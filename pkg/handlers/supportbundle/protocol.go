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

package supportbundle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

const (
	defaultRetryAfter = 2 * time.Second
	minRetryAfter     = 1 * time.Second
	maxRetryAfter     = 30 * time.Second

	// maxErrorBodyBytes bounds how much of an error response is read, for the message, and the
	// error a source reports for a failed bundle.
	maxErrorBodyBytes = 4096
	// maxJSONBodyBytes bounds a source's create and status responses.
	maxJSONBodyBytes = 64 * 1024

	remoteDeleteTimeout = 5 * time.Second
)

var (
	safeNameRegexp = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

	errSourceTooLarge = errors.New("source tarball exceeds supportBundle.maxSourceBytes")
)

// isSafeName reports whether s can be used as a single path segment, both in a URL and on disk.
func isSafeName(s string) bool {
	return safeNameRegexp.MatchString(s) && s != "." && s != ".."
}

// protocolClient implements the antrea-ui side of the support bundle source protocol (see
// docs/supportbundle.md). Every URL is built from the source's configured base and an ID that
// passed isSafeName: nothing a source returns (a Location header in particular) is ever followed.
type protocolClient struct {
	conn     *sourceConn
	maxBytes int64
}

func (p *protocolClient) do(ctx context.Context, method string, u *url.URL, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Set explicitly so that the transport never decompresses a response on its own: a source
	// that labels its .tar.gz with Content-Encoding: gzip must still be stored as returned.
	req.Header.Set("Accept-Encoding", "identity")
	if p.conn.authorize != nil {
		if err := p.conn.authorize(ctx, req); err != nil {
			return nil, err
		}
	}
	// The error (a *url.Error) names the method and URL only, never a header.
	return p.conn.client.Do(req)
}

// sourceMessage bounds a message from a source, which ends up in the bundle's status.
func sourceMessage(msg string) string {
	if len(msg) > maxErrorBodyBytes {
		msg = msg[:maxErrorBodyBytes]
	}
	return strings.TrimSpace(strings.ToValidUTF8(msg, ""))
}

// statusError describes an unexpected response, which fails the source.
func statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	msg := sourceMessage(string(body))
	if msg == "" {
		return fmt.Errorf("source returned HTTP %d", resp.StatusCode)
	}
	return fmt.Errorf("source returned HTTP %d: %s", resp.StatusCode, msg)
}

func decodeBundle(resp *http.Response) (*apisv1.SupportBundle, error) {
	var b apisv1.SupportBundle
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxJSONBodyBytes)).Decode(&b); err != nil {
		return nil, fmt.Errorf("invalid response from source: %w", err)
	}
	return &b, nil
}

// retryAfter reads a Retry-After header given in seconds, clamped so that a source can neither
// make antrea-ui poll it in a tight loop nor stall a collection.
func retryAfter(resp *http.Response) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After")))
	if err != nil {
		return defaultRetryAfter
	}
	d := time.Duration(seconds) * time.Second
	return min(max(d, minRetryAfter), maxRetryAfter)
}

// create asks the source for a new bundle and returns its ID, and how long to wait before polling
// it.
func (p *protocolClient) create(ctx context.Context, request *apisv1.SupportBundleRequest) (string, time.Duration, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return "", 0, err
	}
	resp, err := p.do(ctx, http.MethodPost, p.conn.base.JoinPath("supportbundle"), body)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusAccepted:
	default:
		return "", 0, statusError(resp)
	}
	b, err := decodeBundle(resp)
	if err != nil {
		return "", 0, err
	}
	if !isSafeName(b.ID) {
		return "", 0, fmt.Errorf("source returned an invalid bundle ID %q", b.ID)
	}
	return b.ID, retryAfter(resp), nil
}

// transientError is a failed status poll worth retrying: the source has accepted the bundle, and a
// brief unavailability of the source or of the apiserver in front of it must not discard that work.
type transientError struct {
	err        error
	retryAfter time.Duration
}

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

// status returns the source's bundle, and how long to wait before polling it again. A failure
// worth retrying is a *transientError.
func (p *protocolClient) status(ctx context.Context, id string) (*apisv1.SupportBundle, time.Duration, error) {
	resp, err := p.do(ctx, http.MethodGet, p.conn.base.JoinPath("supportbundle", id, "status"), nil)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, err
		}
		return nil, 0, &transientError{err: err, retryAfter: defaultRetryAfter}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		err := statusError(resp)
		switch resp.StatusCode {
		case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return nil, 0, &transientError{err: err, retryAfter: retryAfter(resp)}
		}
		return nil, 0, err
	}
	b, err := decodeBundle(resp)
	if err != nil {
		return nil, 0, err
	}
	return b, retryAfter(resp), nil
}

// download streams the source's tarball to w, and fails once it exceeds maxBytes.
func (p *protocolClient) download(ctx context.Context, id string, w io.Writer) (int64, error) {
	resp, err := p.do(ctx, http.MethodGet, p.conn.base.JoinPath("supportbundle", id, "download"), nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, statusError(resp)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, p.maxBytes+1))
	if err != nil {
		return n, err
	}
	if n > p.maxBytes {
		return n, errSourceTooLarge
	}
	return n, nil
}

// delete asks the source to drop its bundle. It is best effort, and runs on a context of its own so
// that it still happens when the collection was canceled.
func (p *protocolClient) delete(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), remoteDeleteTimeout)
	defer cancel()
	resp, err := p.do(ctx, http.MethodDelete, p.conn.base.JoinPath("supportbundle", id), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return statusError(resp)
	}
	return nil
}
