// Copyright 2026 Antrea Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package metricstap

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

const (
	// maxScrapeBodyBytes bounds the response of a target. A scrape whose response is larger
	// fails: a silently truncated exposition would be parsed into metrics which look complete.
	maxScrapeBodyBytes = 10 * 1024 * 1024 // 10MiB
	metricsPath        = "/metrics"
	// dialTimeout bounds the connection attempt of a scrape, so that an address which drops
	// packets fails as unreachable well before the scrape as a whole times out.
	dialTimeout = 5 * time.Second
)

// ScraperServiceAccountName is the name of the ServiceAccount whose tokens antrea-ui presents to
// the targets it scrapes. It holds read access to /metrics and nothing else (see
// build/charts/antrea-ui/templates/clusterroles.yaml).
const ScraperServiceAccountName = "antrea-ui-metrics-scraper"

// TokenSource provides the bearer token scrapes authenticate with. It is the token of a
// ServiceAccount which holds nothing but read access to /metrics, never the credential of a user:
// some targets are reached at an address and verified against a CA which they report themselves.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// DialFunc opens the connection of a scrape. Outside of tests, it is the DialContext of a
// net.Dialer whose timeout is dialTimeout.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// ConnInfo is what it takes to reach the HTTPS metrics endpoint of one target.
type ConnInfo struct {
	// Host is the host, and optional port, to connect to.
	Host string
	// ServerName is the name the server certificate must be valid for.
	ServerName string
	// CABundle is the PEM bundle the server certificate must chain to. It is required: there
	// is no fallback to the system roots, and none to skipping verification.
	CABundle []byte
}

// Scraper reads the Prometheus metrics of HTTPS targets.
type Scraper struct {
	tokens TokenSource
	dial   DialFunc
}

// NewScraper creates a Scraper which authenticates with the tokens of tokens and connects with
// dial.
func NewScraper(tokens TokenSource, dial DialFunc) *Scraper {
	return &Scraper{
		tokens: tokens,
		dial:   dial,
	}
}

// Scrape performs one GET of /metrics on the target described by info, and parses the response. It
// is bounded by ctx. A failure is an *Error.
func (s *Scraper) Scrape(ctx context.Context, info ConnInfo) ([]*dto.MetricFamily, error) {
	families, err := s.scrape(ctx, info)
	if err != nil {
		return nil, classifyScrapeError(ctx, err)
	}
	return families, nil
}

func (s *Scraper) scrape(ctx context.Context, info ConnInfo) ([]*dto.MetricFamily, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(info.CABundle) {
		return nil, newError(ErrorCodeInvalidTarget, "the target has no usable CA bundle", nil)
	}

	token, err := s.tokens.Token(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, newError(ErrorCodeCredential, "failed to obtain the credential for scraping", err)
	}

	// A new transport for each scrape, with keep-alives off: nothing is cached, so there is
	// nothing to evict when a target goes away or changes its certificate, and no connection
	// outlives the scrape it was opened for. What this costs is a TLS handshake per scrape, and
	// scrapes are coalesced across taps, which bounds how many there are.
	transport := &http.Transport{
		// The request must go to the target and nowhere else, whatever the environment says.
		Proxy:       nil,
		DialContext: s.dial,
		TLSClientConfig: &tls.Config{
			RootCAs:    roots,
			ServerName: info.ServerName,
			MinVersion: tls.VersionTLS12,
		},
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		// Never follow a redirect: it would carry the token to wherever the target points.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	u := url.URL{Scheme: "https", Host: info.Host, Path: metricsPath}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", string(expfmt.NewFormat(expfmt.TypeTextPlain)))

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, newError(ErrorCodeBadStatus, fmt.Sprintf("the target answered with status %d", resp.StatusCode), nil)
	}

	// Reading one byte past the limit is what tells a response of exactly the limit from one
	// which was cut.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxScrapeBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxScrapeBodyBytes {
		return nil, newError(ErrorCodeTooLarge, "the response of the target exceeds the size limit", nil)
	}

	families, err := decodeFamilies(bytes.NewReader(body), expfmt.ResponseFormat(resp.Header))
	if err != nil {
		return nil, newError(ErrorCodeBadResponse, "the response of the target is not valid Prometheus metrics", err)
	}
	return families, nil
}

// decodeFamilies parses an exposition and returns its families sorted by name.
func decodeFamilies(r io.Reader, format expfmt.Format) ([]*dto.MetricFamily, error) {
	decoder := expfmt.NewDecoder(r, format)
	var families []*dto.MetricFamily
	for {
		family := &dto.MetricFamily{}
		if err := decoder.Decode(family); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		families = append(families, family)
	}
	sort.Slice(families, func(i, j int) bool { return families[i].GetName() < families[j].GetName() })
	return families, nil
}

// classifyScrapeError turns the failure of a scrape into an *Error, whose message names the class
// of the failure only. The error of a dial or of a handshake holds the address and sometimes the
// certificate of the target, which a client has no business seeing.
func classifyScrapeError(ctx context.Context, err error) *Error {
	if scrapeErr, ok := errors.AsType[*Error](err); ok {
		return scrapeErr
	}
	if ctx.Err() != nil {
		return newError(ErrorCodeTimeout, "the scrape timed out", err)
	}
	if isTLSError(err) {
		return newError(ErrorCodeTLS, "the TLS handshake with the target failed", err)
	}
	// This comes before the checks for a timeout, which the error of a connection attempt that
	// the dialer gave up on matches too: the scrape did not time out, the target is unreachable.
	if opErr, ok := errors.AsType[*net.OpError](err); ok && opErr.Op == "dial" {
		return newError(ErrorCodeUnreachable, "failed to connect to the target", err)
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return newError(ErrorCodeTimeout, "the scrape timed out", err)
	}
	if netErr, ok := errors.AsType[net.Error](err); ok && netErr.Timeout() {
		return newError(ErrorCodeTimeout, "the scrape timed out", err)
	}
	return newError(ErrorCodeFailed, "the scrape failed", err)
}

func isTLSError(err error) bool {
	if _, ok := errors.AsType[*tls.CertificateVerificationError](err); ok {
		return true
	}
	if _, ok := errors.AsType[x509.UnknownAuthorityError](err); ok {
		return true
	}
	if _, ok := errors.AsType[x509.HostnameError](err); ok {
		return true
	}
	if _, ok := errors.AsType[x509.CertificateInvalidError](err); ok {
		return true
	}
	if _, ok := errors.AsType[tls.RecordHeaderError](err); ok {
		return true
	}
	// An alert sent by the target. It is not a tls.AlertError, which only QUIC connections
	// return.
	if opErr, ok := errors.AsType[*net.OpError](err); ok && opErr.Op == "remote error" {
		return true
	}
	return false
}
