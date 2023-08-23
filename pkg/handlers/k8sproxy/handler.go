// Copyright 2023 Antrea Authors.
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

package k8sproxy

import (
	"errors"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/go-logr/logr"
)

type transportWrapper struct {
	logger logr.Logger
	t      http.RoundTripper
}

func (w *transportWrapper) RoundTrip(r *http.Request) (*http.Response, error) {
	w.logger.V(4).Info("Proxying request", "url", r.URL)
	return w.t.RoundTrip(r)
}

type proxyErrorWriter struct {
	logger logr.Logger
}

func (w *proxyErrorWriter) Write(p []byte) (n int, err error) {
	w.logger.Error(errors.New(string(p)), "K8s proxy error")
	return len(p), nil
}

func NewK8sProxyHandler(logger logr.Logger, k8sServerURL *url.URL, k8sHTTPTransport http.RoundTripper) http.Handler {
	errorLogger := log.New(&proxyErrorWriter{
		logger: logger,
	}, "", 0)

	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(k8sServerURL) // Also rewrites the Host header.
			r.Out.Header["X-Forwarded-For"] = r.In.Header["X-Forwarded-For"]
			r.SetXForwarded() // Set X-Forwarded-* headers.
		},
		Transport: &transportWrapper{
			logger: logger,
			t:      k8sHTTPTransport,
		},
		ErrorLog: errorLogger,
	}
}
