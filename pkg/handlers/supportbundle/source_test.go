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
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	serverconfig "antrea.io/antrea-ui/pkg/config/server"
)

// These tests talk to a real TLS server, which a synctest bubble cannot host, so they exercise
// connect and single protocol calls rather than a whole collection: what matters here is what
// reaches the source.

type receivedRequest struct {
	method     string
	header     http.Header
	serverName string
}

func newTLSSourceServer(t *testing.T) (*httptest.Server, func() []receivedRequest) {
	var mutex sync.Mutex
	var received []receivedRequest
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutex.Lock()
		received = append(received, receivedRequest{method: r.Method, header: r.Header.Clone(), serverName: r.TLS.ServerName})
		mutex.Unlock()
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusAccepted)
		}
		_ = json.NewEncoder(w).Encode(apisv1.SupportBundle{ID: "remote-1", Status: apisv1.SupportBundleStatusCollected})
	}))
	t.Cleanup(srv.Close)
	return srv, func() []receivedRequest {
		mutex.Lock()
		defer mutex.Unlock()
		return append([]receivedRequest(nil), received...)
	}
}

func httpsConfig(srv *httptest.Server) *serverconfig.SupportBundleHTTPSSource {
	caData := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	return &serverconfig.SupportBundleHTTPSSource{URL: srv.URL + "/api/v1", CAData: string(caData)}
}

// fakeTokenSource hands out a token naming the audience it was built for.
type fakeTokenSource struct{ audience string }

func (f *fakeTokenSource) Token(context.Context) (string, error) {
	return "token-for-" + f.audience, nil
}

func newFakeTokenSource(audience string) TokenSource { return &fakeTokenSource{audience: audience} }

var testCollection = requester{username: testRequester, bundleID: "bundle-1"}

// createOnce connects to s and sends a create request.
func createOnce(t *testing.T, s source) error {
	p := &protocolClient{conn: s.connect(testCollection), maxBytes: 1024}
	id, _, err := p.create(t.Context(), &apisv1.SupportBundleRequest{})
	if err == nil {
		assert.Equal(t, "remote-1", id)
	}
	return err
}

// An https source gets, on every request, a token minted for antrea-ui-admin with its own
// audience, and the audit headers naming the requester and the bundle.
func TestHTTPSSourceRequestsCarryAdminTokenAndAuditHeaders(t *testing.T) {
	srv, received := newTLSSourceServer(t)
	config := testConfig(t)
	config.ExtraSources = []serverconfig.SupportBundleExtraSource{{Name: "foo", HTTPS: httpsConfig(srv)}}
	mi, err := NewManager(Options{Logger: testr.New(t), Config: config, NewAdminTokenSource: newFakeTokenSource})
	require.NoError(t, err)
	m := mi.(*manager)
	require.Len(t, m.extraSources, 1)

	const username = "alice@example.com/ü x"
	p := &protocolClient{conn: m.extraSources[0].connect(requester{username: username, bundleID: "bundle-1"}), maxBytes: 1024}
	id, _, err := p.create(t.Context(), &apisv1.SupportBundleRequest{})
	require.NoError(t, err)
	_, _, err = p.status(t.Context(), id)
	require.NoError(t, err)
	_, err = p.download(t.Context(), id, &bytes.Buffer{})
	require.NoError(t, err)
	require.NoError(t, p.delete(id))

	requests := received()
	require.Len(t, requests, 4)
	for _, r := range requests {
		assert.Equal(t, "Bearer token-for-supportbundle.ui.antrea.io/foo", r.header.Get("Authorization"), r.method)
		assert.Equal(t, "alice@example.com%2F%C3%BC%20x", r.header.Get("X-Antrea-UI-Requested-By"), r.method)
		assert.Equal(t, "bundle-1", r.header.Get("X-Antrea-UI-Support-Bundle"), r.method)
	}
}

// An apiServer source tells the apiserver who requested the bundle through a user extra, whose
// value is percent-encoded: a header value cannot hold every character a username can.
func TestAPIServerSourcePercentEncodesRequester(t *testing.T) {
	var header http.Header
	rt := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		header = req.Header.Clone()
		return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(`{"id":"remote-1","status":"Collecting"}`)), Request: req}, nil
	})
	s := newAPIServerSource("foo", apisv1.SupportBundleSourceKindExtra, &url.URL{Scheme: "https", Host: testAPIServerHost}, "/apis/foo.example.com/v1", rt, testAdminUser)
	p := &protocolClient{conn: s.connect(requester{username: "alice\n/ü x", bundleID: "bundle-1"}), maxBytes: 1024}
	_, _, err := p.create(t.Context(), &apisv1.SupportBundleRequest{})
	require.NoError(t, err)
	assert.Equal(t, []string{"alice%0A%2F%C3%BC%20x"}, header.Values("Impersonate-Extra-supportbundle.ui.antrea.io%2Frequested-by"))
	assert.Equal(t, []string{"bundle-1"}, header.Values("Impersonate-Extra-supportbundle.ui.antrea.io%2Fbundle-id"))
}

// A transport error on a status poll is retried, unless the collection is over.
func TestStatusTransportErrorIsTransient(t *testing.T) {
	rt := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("connection reset")
	})
	s := newAPIServerSource("foo", apisv1.SupportBundleSourceKindExtra, &url.URL{Scheme: "https", Host: testAPIServerHost}, "/apis/foo.example.com/v1", rt, testAdminUser)
	p := &protocolClient{conn: s.connect(testCollection), maxBytes: 1024}
	_, _, err := p.status(t.Context(), "remote-1")
	var transient *transientError
	assert.ErrorAs(t, err, &transient)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err = p.status(ctx, "remote-1")
	require.Error(t, err)
	assert.NotErrorAs(t, err, &transient)
}

func TestNewManagerRequiresTokenSourceForHTTPSSource(t *testing.T) {
	config := testConfig(t)
	config.ExtraSources = []serverconfig.SupportBundleExtraSource{{Name: "foo", HTTPS: &serverconfig.SupportBundleHTTPSSource{URL: "https://foo.example.com"}}}
	_, err := NewManager(Options{Logger: testr.New(t), Config: config})
	assert.ErrorContains(t, err, "no admin token source")

	// Sources reached through the apiserver do not need one.
	config.ExtraSources = []serverconfig.SupportBundleExtraSource{apiServerExtraSource("bar", "bar.example.com")}
	_, err = NewManager(Options{Logger: testr.New(t), Config: config, APIServerURL: &url.URL{Scheme: "https", Host: testAPIServerHost}})
	assert.NoError(t, err)
}

func TestHTTPSSourceVerifiesServerCertificate(t *testing.T) {
	srv, received := newTLSSourceServer(t)
	config := httpsConfig(srv)
	config.CAData = ""
	s, err := newHTTPSSource(testr.New(t), "foo", config, newFakeTokenSource("foo"))
	require.NoError(t, err)
	assert.ErrorContains(t, createOnce(t, s), "certificate")
	assert.Empty(t, received())

	config.InsecureSkipVerify = true
	s, err = newHTTPSSource(testr.New(t), "foo", config, newFakeTokenSource("foo"))
	require.NoError(t, err)
	require.NoError(t, createOnce(t, s))
	assert.Len(t, received(), 1)
}

func TestHTTPSSourceServerName(t *testing.T) {
	srv, received := newTLSSourceServer(t)
	config := httpsConfig(srv)
	// The httptest certificate is valid for example.com as well as for 127.0.0.1.
	config.ServerName = "example.com"
	s, err := newHTTPSSource(testr.New(t), "foo", config, newFakeTokenSource("foo"))
	require.NoError(t, err)
	require.NoError(t, createOnce(t, s))
	require.Len(t, received(), 1)
	assert.Equal(t, "example.com", received()[0].serverName)
}

// A source that labels its .tar.gz with Content-Encoding: gzip, as a file server may, must have its
// tarball stored as returned, not transparently decompressed by the transport.
func TestHTTPSSourceDownloadIsNotDecompressed(t *testing.T) {
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	_, err := gz.Write([]byte("not really a tarball"))
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressed.Bytes())
	}))
	t.Cleanup(srv.Close)

	s, err := newHTTPSSource(testr.New(t), "foo", httpsConfig(srv), newFakeTokenSource("foo"))
	require.NoError(t, err)
	p := &protocolClient{conn: s.connect(testCollection), maxBytes: 1024}
	var downloaded bytes.Buffer
	_, err = p.download(t.Context(), "remote-1", &downloaded)
	require.NoError(t, err)
	assert.Equal(t, compressed.Bytes(), downloaded.Bytes())
}
