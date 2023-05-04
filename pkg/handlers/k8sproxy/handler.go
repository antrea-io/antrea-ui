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

func NewK8sProxyHandler(logger logr.Logger, k8sServerURL *url.URL, k8sHTTPTransport http.RoundTripper) http.Handler {
	// TODO: the httputil.ReverseProxy is much improved in Go v1.20, but we currently use Go
	// v1.19. When we upgrade, we should revisit this code.
	k8sReverseProxy := httputil.NewSingleHostReverseProxy(k8sServerURL)
	k8sReverseProxy.Transport = &transportWrapper{
		logger: logger,
		t:      k8sHTTPTransport,
	}
	return k8sReverseProxy
}
