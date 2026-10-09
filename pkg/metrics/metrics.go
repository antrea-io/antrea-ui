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

// Package metrics holds the Prometheus metrics the Antrea UI backend exposes about itself.
package metrics

import (
	"net/http"
	"runtime"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"antrea.io/antrea-ui/pkg/version"
)

const namespace = "antrea_ui"

// unmatchedPath is the "path" label of a request which matched no route. The raw URL path is never
// used as a label: it is chosen by the client, so it would make the cardinality unbounded.
const unmatchedPath = "unmatched"

// Metrics is the backend's own instrumentation. It uses a dedicated registry and not the global
// default one, so that what the backend exposes is exactly what is registered here.
type Metrics struct {
	registry *prometheus.Registry

	httpRequests        *prometheus.CounterVec
	httpRequestDuration *prometheus.HistogramVec
	openTaps            prometheus.Gauge
	tapScrapes          *prometheus.CounterVec
}

// New creates the registry and registers every collector on it.
func New() *Metrics {
	m := &Metrics{
		registry: prometheus.NewRegistry(),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "http_requests_total",
			Help:      "Number of HTTP requests handled by the backend, partitioned by method, route and status code.",
		}, []string{"method", "path", "code"}),
		httpRequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "http_request_duration_seconds",
			Help:      "Duration of the HTTP requests handled by the backend, partitioned by method and route. Streaming routes are not observed.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"method", "path"}),
		openTaps: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "metrics_taps_open",
			Help:      "Number of metrics taps currently open.",
		}),
		tapScrapes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "metrics_tap_scrapes_total",
			Help:      "Number of scrapes performed for metrics taps, partitioned by target group and outcome. A scrape shared by several taps counts once.",
		}, []string{"target_group", "outcome"}),
	}
	buildInfo := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "build_info",
		Help:      "A metric with a constant value of 1, labeled with the version of the backend and the Go version it was built with.",
		ConstLabels: prometheus.Labels{
			"version":   version.GetFullVersion(),
			"goversion": runtime.Version(),
		},
	})
	buildInfo.Set(1)
	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		buildInfo,
		m.httpRequests,
		m.httpRequestDuration,
		m.openTaps,
		m.tapScrapes,
	)
	return m
}

// Gatherer gives read access to the registry.
func (m *Metrics) Gatherer() prometheus.Gatherer {
	return m.registry
}

// HandlerFor returns the handler which serves gatherer in the Prometheus exposition format.
func HandlerFor(gatherer prometheus.Gatherer) http.Handler {
	return promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{})
}

// GinMiddleware counts every request, and observes its duration unless its route is one of
// streamingRoutes (as returned by gin's FullPath): a stream lasts for as long as the client stays
// connected, which says nothing about the backend.
//
// It must be registered before gin.Recovery, or the request of a handler which panics is not
// counted.
func (m *Metrics) GinMiddleware(streamingRoutes ...string) gin.HandlerFunc {
	streaming := make(map[string]bool, len(streamingRoutes))
	for _, route := range streamingRoutes {
		streaming[route] = true
	}
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		path := c.FullPath()
		if path == "" {
			path = unmatchedPath
		}
		method := methodLabel(c.Request.Method)
		m.httpRequests.WithLabelValues(method, path, strconv.Itoa(c.Writer.Status())).Inc()
		if !streaming[path] {
			m.httpRequestDuration.WithLabelValues(method, path).Observe(time.Since(start).Seconds())
		}
	}
}

// methodLabel bounds the "method" label: like the path, the method of a request which matched no
// route is whatever the client sent.
func methodLabel(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions:
		return method
	default:
		return "other"
	}
}

// TapOpened records that a metrics tap was opened.
func (m *Metrics) TapOpened() {
	m.openTaps.Inc()
}

// TapClosed records that a metrics tap ended.
func (m *Metrics) TapClosed() {
	m.openTaps.Dec()
}

// ObserveScrape records one scrape of a target of targetGroup. outcome is "success" or the code
// of the failure.
func (m *Metrics) ObserveScrape(targetGroup, outcome string) {
	m.tapScrapes.WithLabelValues(targetGroup, outcome).Inc()
}
