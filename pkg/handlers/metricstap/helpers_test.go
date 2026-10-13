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
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/madflojo/testcerts"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/test/bufconn"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

// testToken is the bearer token the scrapers of the tests authenticate with.
const testToken = "scraper-token"

type staticTokenSource struct {
	token string
	err   error
	calls atomic.Int32
}

func (s *staticTokenSource) Token(context.Context) (string, error) {
	s.calls.Add(1)
	return s.token, s.err
}

// memoryListener is a net.Listener whose connections are in-memory and buffered, handed out by
// dial. A server behind it is not reachable through any real address, and neither side ever blocks
// on real network I/O, so it can also be used in a testing/synctest bubble.
type memoryListener struct {
	*bufconn.Listener

	mutex sync.Mutex
	// dialed records the address of every dial, in order.
	dialed []string
}

func newMemoryListener() *memoryListener {
	return &memoryListener{Listener: bufconn.Listen(1024 * 1024)}
}

// dial is the DialFunc of the scraper under test. Whatever the address, the connection goes to the
// server behind the listener.
func (l *memoryListener) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	l.mutex.Lock()
	l.dialed = append(l.dialed, addr)
	l.mutex.Unlock()
	conn, err := l.DialContext(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &net.OpError{Op: "dial", Net: network, Err: err}
	}
	return conn, nil
}

func (l *memoryListener) dialedAddrs() []string {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	return append([]string(nil), l.dialed...)
}

// metricsServer is an HTTPS server on in-memory connections, standing in for the metrics endpoint
// of an Antrea component.
type metricsServer struct {
	*memoryListener
	// caBundle is the PEM bundle of the CA which issued the certificate of the server.
	caBundle []byte

	mutex sync.Mutex
	cert  *tls.Certificate
}

// newMetricsServer starts a server whose certificate is issued for serverName by a new CA. It
// must be called from the test (or bubble) which uses the server: the certificates are valid
// around the current time, which is not the real one in a bubble. configure can change the TLS
// configuration of the server.
func newMetricsServer(t *testing.T, serverName string, handler http.Handler, configure ...func(*tls.Config)) *metricsServer {
	t.Helper()
	s := &metricsServer{memoryListener: newMemoryListener()}
	s.rotateCertificate(t, serverName)
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		// It would only report the failed handshakes which some tests cause on purpose.
		ErrorLog: log.New(io.Discard, "", 0),
	}
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			s.mutex.Lock()
			defer s.mutex.Unlock()
			return s.cert, nil
		},
	}
	for _, fn := range configure {
		fn(tlsConfig)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(tls.NewListener(s.memoryListener, tlsConfig))
	}()
	t.Cleanup(func() {
		_ = srv.Close()
		<-done
	})
	return s
}

// rotateCertificate gives the server a certificate issued by a new CA, as an agent which
// restarts does, and updates caBundle.
func (s *metricsServer) rotateCertificate(t *testing.T, serverName string) {
	t.Helper()
	ca := testcerts.NewCA()
	kp, err := ca.NewKeyPair(serverName)
	require.NoError(t, err)
	cert, err := tls.X509KeyPair(kp.PublicKey(), kp.PrivateKey())
	require.NoError(t, err)
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.cert = &cert
	s.caBundle = ca.PublicKey()
}

// expositionHandler serves body as a Prometheus text exposition, and records the requests.
type expositionHandler struct {
	body string

	mutex    sync.Mutex
	requests []*http.Request
}

func (h *expositionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mutex.Lock()
	h.requests = append(h.requests, r)
	h.mutex.Unlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = w.Write([]byte(h.body))
}

func (h *expositionHandler) received() []*http.Request {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	return append([]*http.Request(nil), h.requests...)
}

// fakeTargetGroup is a TargetGroup whose scrapes are answered by a function.
type fakeTargetGroup struct {
	name      string
	instanced bool
	targets   []apisv1.MetricsTarget
	targetErr error

	mutex  sync.Mutex
	scrape func(ctx context.Context, instance string) ([]*dto.MetricFamily, error)
	// scrapes counts the scrapes received, per instance.
	scrapes map[string]int
}

func newFakeTargetGroup(name string, instanced bool, scrape func(ctx context.Context, instance string) ([]*dto.MetricFamily, error)) *fakeTargetGroup {
	return &fakeTargetGroup{name: name, instanced: instanced, scrape: scrape, scrapes: make(map[string]int)}
}

func (g *fakeTargetGroup) Name() string    { return g.name }
func (g *fakeTargetGroup) Instanced() bool { return g.instanced }

func (g *fakeTargetGroup) Targets(context.Context) ([]apisv1.MetricsTarget, error) {
	return g.targets, g.targetErr
}

func (g *fakeTargetGroup) Scrape(ctx context.Context, instance string) ([]*dto.MetricFamily, error) {
	g.mutex.Lock()
	g.scrapes[instance]++
	scrape := g.scrape
	g.mutex.Unlock()
	return scrape(ctx, instance)
}

func (g *fakeTargetGroup) setScrape(scrape func(ctx context.Context, instance string) ([]*dto.MetricFamily, error)) {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	g.scrape = scrape
}

func (g *fakeTargetGroup) scrapeCount(instance string) int {
	g.mutex.Lock()
	defer g.mutex.Unlock()
	return g.scrapes[instance]
}

// gaugeFamilies returns one gauge family per name, each with a single series of the given value.
func gaugeFamilies(value float64, names ...string) []*dto.MetricFamily {
	families := make([]*dto.MetricFamily, 0, len(names))
	for _, name := range names {
		families = append(families, &dto.MetricFamily{
			Name:   new(name),
			Type:   dto.MetricType_GAUGE.Enum(),
			Metric: []*dto.Metric{{Gauge: &dto.Gauge{Value: new(value)}}},
		})
	}
	return families
}
