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
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testServerName = "localhost"
	// testHost is an address no test can reach for real: connections go through the in-memory
	// listener, whatever the address.
	testHost = "192.0.2.10:10350"

	testExposition = `# HELP antrea_agent_local_pod_count Number of Pods on the local Node.
# TYPE antrea_agent_local_pod_count gauge
antrea_agent_local_pod_count 7
# HELP antrea_agent_ovs_flow_count Flow count for each OVS flow table.
# TYPE antrea_agent_ovs_flow_count gauge
antrea_agent_ovs_flow_count{table_id="0",table_name="Classifier"} 5
antrea_agent_ovs_flow_count{table_id="1",table_name="SpoofGuard"} 12
`
)

// requireScrapeError asserts that err is an *Error with the given code, whose message for clients
// does not give away where the scrape went.
func requireScrapeError(t *testing.T, err error, code string) *Error {
	t.Helper()
	require.Error(t, err)
	scrapeErr, ok := errors.AsType[*Error](err)
	require.True(t, ok, "expected an *Error, got %T: %v", err, err)
	assert.Equal(t, code, scrapeErr.Code)
	assert.NotContains(t, scrapeErr.Message, "192.0.2.10")
	return scrapeErr
}

func TestScraper(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		handler := &expositionHandler{body: testExposition}
		server := newMetricsServer(t, testServerName, handler)
		scraper := NewScraper(&staticTokenSource{token: testToken}, server.dial)

		families, err := scraper.Scrape(t.Context(), ConnInfo{Host: testHost, ServerName: testServerName, CABundle: server.caBundle})
		require.NoError(t, err)
		require.Len(t, families, 2)
		// Sorted by name, whatever the order of the exposition.
		assert.Equal(t, "antrea_agent_local_pod_count", families[0].GetName())
		assert.Equal(t, "antrea_agent_ovs_flow_count", families[1].GetName())
		assert.Len(t, families[1].GetMetric(), 2)

		assert.Equal(t, []string{testHost}, server.dialedAddrs())
		requests := handler.received()
		require.Len(t, requests, 1)
		assert.Equal(t, http.MethodGet, requests[0].Method)
		assert.Equal(t, "/metrics", requests[0].URL.Path)
		assert.Equal(t, "Bearer "+testToken, requests[0].Header.Get("Authorization"))
		assert.Equal(t, testServerName, requests[0].TLS.ServerName)
	})

	t.Run("certificate from another CA", func(t *testing.T) {
		handler := &expositionHandler{body: testExposition}
		server := newMetricsServer(t, testServerName, handler)
		pinned := server.caBundle
		server.rotateCertificate(t, testServerName)
		scraper := NewScraper(&staticTokenSource{token: testToken}, server.dial)

		_, err := scraper.Scrape(t.Context(), ConnInfo{Host: testHost, ServerName: testServerName, CABundle: pinned})
		requireScrapeError(t, err, ErrorCodeTLS)
		// The token is only sent once the server has proved who it is.
		assert.Empty(t, handler.received())
	})

	t.Run("certificate for another name", func(t *testing.T) {
		handler := &expositionHandler{body: testExposition}
		server := newMetricsServer(t, "antrea.kube-system.svc", handler)
		scraper := NewScraper(&staticTokenSource{token: testToken}, server.dial)

		_, err := scraper.Scrape(t.Context(), ConnInfo{Host: testHost, ServerName: testServerName, CABundle: server.caBundle})
		requireScrapeError(t, err, ErrorCodeTLS)
		assert.Empty(t, handler.received())
	})

	t.Run("no CA bundle", func(t *testing.T) {
		server := newMetricsServer(t, testServerName, &expositionHandler{body: testExposition})
		tokens := &staticTokenSource{token: testToken}
		scraper := NewScraper(tokens, server.dial)

		for _, bundle := range [][]byte{nil, []byte("not a certificate")} {
			_, err := scraper.Scrape(t.Context(), ConnInfo{Host: testHost, ServerName: testServerName, CABundle: bundle})
			requireScrapeError(t, err, ErrorCodeInvalidTarget)
		}
		// There is no fallback to an unverified connection: nothing is even dialed.
		assert.Empty(t, server.dialedAddrs())
		assert.Zero(t, tokens.calls.Load())
	})

	t.Run("redirect is not followed", func(t *testing.T) {
		var requests []string
		server := newMetricsServer(t, testServerName, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests = append(requests, r.URL.Path)
			http.Redirect(w, r, "https://elsewhere.invalid/metrics", http.StatusFound)
		}))
		scraper := NewScraper(&staticTokenSource{token: testToken}, server.dial)

		_, err := scraper.Scrape(t.Context(), ConnInfo{Host: testHost, ServerName: testServerName, CABundle: server.caBundle})
		scrapeErr := requireScrapeError(t, err, ErrorCodeBadStatus)
		assert.Contains(t, scrapeErr.Message, "302")
		assert.Equal(t, []string{"/metrics"}, requests)
		assert.Equal(t, []string{testHost}, server.dialedAddrs())
	})

	t.Run("status other than 200", func(t *testing.T) {
		server := newMetricsServer(t, testServerName, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "forbidden: User \"system:serviceaccount:kube-system:antrea-ui-metrics-scraper\" cannot get path \"/metrics\"", http.StatusForbidden)
		}))
		scraper := NewScraper(&staticTokenSource{token: testToken}, server.dial)

		_, err := scraper.Scrape(t.Context(), ConnInfo{Host: testHost, ServerName: testServerName, CABundle: server.caBundle})
		scrapeErr := requireScrapeError(t, err, ErrorCodeBadStatus)
		assert.Contains(t, scrapeErr.Message, "403")
		// The body of the target is not relayed.
		assert.NotContains(t, scrapeErr.Error(), "serviceaccount")
	})

	t.Run("response over the size limit", func(t *testing.T) {
		// The exposition, followed by a comment which brings it to exactly the limit.
		const padding = "# "
		atLimit := testExposition + padding + strings.Repeat("x", maxScrapeBodyBytes-len(testExposition)-len(padding)-1) + "\n"
		require.Len(t, atLimit, maxScrapeBodyBytes)
		handler := &expositionHandler{body: atLimit}
		server := newMetricsServer(t, testServerName, handler)
		scraper := NewScraper(&staticTokenSource{token: testToken}, server.dial)

		// Exactly at the limit is fine.
		families, err := scraper.Scrape(t.Context(), ConnInfo{Host: testHost, ServerName: testServerName, CABundle: server.caBundle})
		require.NoError(t, err)
		assert.Len(t, families, 2)

		// One byte over fails, and what fits is not returned.
		handler.body = atLimit[:maxScrapeBodyBytes-1] + "x\n"
		_, err = scraper.Scrape(t.Context(), ConnInfo{Host: testHost, ServerName: testServerName, CABundle: server.caBundle})
		requireScrapeError(t, err, ErrorCodeTooLarge)
	})

	t.Run("response which is not metrics", func(t *testing.T) {
		server := newMetricsServer(t, testServerName, &expositionHandler{body: "<html>not metrics</html>\n"})
		scraper := NewScraper(&staticTokenSource{token: testToken}, server.dial)

		_, err := scraper.Scrape(t.Context(), ConnInfo{Host: testHost, ServerName: testServerName, CABundle: server.caBundle})
		requireScrapeError(t, err, ErrorCodeBadResponse)
	})

	t.Run("no token", func(t *testing.T) {
		handler := &expositionHandler{body: testExposition}
		server := newMetricsServer(t, testServerName, handler)
		scraper := NewScraper(&staticTokenSource{err: errors.New("serviceaccounts \"antrea-ui-metrics-scraper\" not found")}, server.dial)

		_, err := scraper.Scrape(t.Context(), ConnInfo{Host: testHost, ServerName: testServerName, CABundle: server.caBundle})
		scrapeErr := requireScrapeError(t, err, ErrorCodeCredential)
		assert.NotContains(t, scrapeErr.Message, "serviceaccounts")
		assert.Empty(t, server.dialedAddrs())
	})

	t.Run("handshake rejected by the target", func(t *testing.T) {
		handler := &expositionHandler{body: testExposition}
		// The target does not go beyond TLS 1.1, which the scraper does not accept: it answers
		// the handshake with an alert.
		server := newMetricsServer(t, testServerName, handler, func(c *tls.Config) {
			c.MinVersion = tls.VersionTLS10
			c.MaxVersion = tls.VersionTLS11
		})
		scraper := NewScraper(&staticTokenSource{token: testToken}, server.dial)

		_, err := scraper.Scrape(t.Context(), ConnInfo{Host: testHost, ServerName: testServerName, CABundle: server.caBundle})
		scrapeErr := requireScrapeError(t, err, ErrorCodeTLS)
		assert.Contains(t, scrapeErr.Error(), "remote error")
		assert.Empty(t, handler.received())
	})

	t.Run("connection refused", func(t *testing.T) {
		server := newMetricsServer(t, testServerName, &expositionHandler{body: testExposition})
		dial := func(_ context.Context, network, addr string) (net.Conn, error) {
			return nil, &net.OpError{Op: "dial", Net: network, Addr: &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 10350}, Err: errors.New("connect: connection refused")}
		}
		scraper := NewScraper(&staticTokenSource{token: testToken}, dial)

		_, err := scraper.Scrape(t.Context(), ConnInfo{Host: testHost, ServerName: testServerName, CABundle: server.caBundle})
		scrapeErr := requireScrapeError(t, err, ErrorCodeUnreachable)
		// The details are kept for the log.
		assert.Contains(t, scrapeErr.Error(), "connection refused")
	})
}

// A connection attempt which the dialer gives up on is a target which cannot be reached, not a
// scrape which timed out, although its error is a timeout.
func TestScraperDialTimeout(t *testing.T) {
	server := newMetricsServer(t, testServerName, &expositionHandler{body: testExposition})
	// With its deadline in the past, the dialer fails as it does when a connection attempt
	// reaches its timeout, without attempting one.
	dialer := &net.Dialer{Deadline: time.Now().Add(-time.Second)}
	scraper := NewScraper(&staticTokenSource{token: testToken}, dialer.DialContext)

	_, err := scraper.Scrape(t.Context(), ConnInfo{Host: testHost, ServerName: testServerName, CABundle: server.caBundle})
	scrapeErr := requireScrapeError(t, err, ErrorCodeUnreachable)
	assert.ErrorIs(t, scrapeErr, context.DeadlineExceeded)
}

func TestScraperTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const timeout = 3 * time.Second
		// The target accepts the request and never answers.
		server := newMetricsServer(t, testServerName, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		scraper := NewScraper(&staticTokenSource{token: testToken}, server.dial)

		ctx, cancel := context.WithTimeout(t.Context(), timeout)
		defer cancel()
		start := time.Now()
		_, err := scraper.Scrape(ctx, ConnInfo{Host: testHost, ServerName: testServerName, CABundle: server.caBundle})
		requireScrapeError(t, err, ErrorCodeTimeout)
		assert.Equal(t, timeout, time.Since(start))
	})
}
