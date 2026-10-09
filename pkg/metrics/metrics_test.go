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

package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() {
	// avoid verbose Gin logging
	gin.SetMode(gin.ReleaseMode)
}

// series returns the series of the family with the given name, keyed by their label values in the
// order of labelNames, joined with spaces.
func series(t *testing.T, m *Metrics, name string, labelNames ...string) map[string]*dto.Metric {
	t.Helper()
	families, err := m.Gatherer().Gather()
	require.NoError(t, err)
	result := map[string]*dto.Metric{}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			var key []string
			for _, labelName := range labelNames {
				key = append(key, labels[labelName])
			}
			result[strings.Join(key, " ")] = metric
		}
	}
	return result
}

func TestRegistry(t *testing.T) {
	m := New()
	families, err := m.Gatherer().Gather()
	require.NoError(t, err)
	names := map[string]bool{}
	for _, family := range families {
		names[family.GetName()] = true
	}
	// The Go and process collectors, and the metrics which have a value before anything
	// happened. The others appear with their first observation.
	for _, name := range []string{"go_goroutines", "process_start_time_seconds", "antrea_ui_build_info", "antrea_ui_metrics_taps_open"} {
		assert.Contains(t, names, name)
	}
}

func TestHandlerFor(t *testing.T) {
	m := New()
	rr := httptest.NewRecorder()
	HandlerFor(m.Gatherer()).ServeHTTP(rr, httptest.NewRequest("GET", "/metrics", nil))
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Header().Get("Content-Type"), "text/plain")
	assert.Contains(t, rr.Body.String(), "antrea_ui_build_info{")
}

func TestGinMiddleware(t *testing.T) {
	const streamRoute = "/api/v1/stream"
	m := New()
	router := gin.New()
	router.Use(m.GinMiddleware(streamRoute))
	router.GET("/api/v1/things/:name", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.POST("/api/v1/things/:name", func(c *gin.Context) { c.Status(http.StatusForbidden) })
	router.GET(streamRoute, func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, req := range []*http.Request{
		httptest.NewRequest("GET", "/api/v1/things/a", nil),
		httptest.NewRequest("GET", "/api/v1/things/b", nil),
		httptest.NewRequest("POST", "/api/v1/things/a", nil),
		httptest.NewRequest("GET", streamRoute, nil),
		// What the client chooses must not become a label value.
		httptest.NewRequest("GET", "/no/such/route", nil),
		httptest.NewRequest("BREW", "/no/such/route/either", nil),
	} {
		router.ServeHTTP(httptest.NewRecorder(), req)
	}

	// The route and not the path: two things, one series.
	requests := series(t, m, "antrea_ui_http_requests_total", "method", "path", "code")
	require.Len(t, requests, 5)
	for key, count := range map[string]float64{
		"GET /api/v1/things/:name 200":  2,
		"POST /api/v1/things/:name 403": 1,
		"GET " + streamRoute + " 200":   1,
		"GET unmatched 404":             1,
		"other unmatched 404":           1,
	} {
		require.Contains(t, requests, key)
		assert.Equal(t, count, requests[key].GetCounter().GetValue(), key)
	}

	// Every route but the streaming one has its duration observed.
	durations := series(t, m, "antrea_ui_http_request_duration_seconds", "method", "path")
	require.Len(t, durations, 4)
	require.Contains(t, durations, "GET /api/v1/things/:name")
	assert.EqualValues(t, 2, durations["GET /api/v1/things/:name"].GetHistogram().GetSampleCount())
	assert.NotContains(t, durations, "GET "+streamRoute)
}

// Registered before gin.Recovery, the middleware counts the request of a handler which panics,
// with the status Recovery answers it with.
func TestGinMiddlewareCountsPanics(t *testing.T) {
	m := New()
	router := gin.New()
	router.Use(m.GinMiddleware(), gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, _ any) {
		c.AbortWithStatus(http.StatusInternalServerError)
	}))
	router.GET("/api/v1/broken", func(*gin.Context) { panic("broken") })

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/broken", nil))
	require.Equal(t, http.StatusInternalServerError, rr.Code)

	requests := series(t, m, "antrea_ui_http_requests_total", "method", "path", "code")
	require.Contains(t, requests, "GET /api/v1/broken 500")
	assert.Equal(t, 1.0, requests["GET /api/v1/broken 500"].GetCounter().GetValue())
	durations := series(t, m, "antrea_ui_http_request_duration_seconds", "method", "path")
	require.Contains(t, durations, "GET /api/v1/broken")
}

func TestTapObserver(t *testing.T) {
	m := New()
	m.TapOpened()
	m.TapOpened()
	m.TapClosed()
	assert.Equal(t, 1.0, series(t, m, "antrea_ui_metrics_taps_open")[""].GetGauge().GetValue())

	m.ObserveScrape("antrea-agent", "success")
	m.ObserveScrape("antrea-agent", "success")
	m.ObserveScrape("antrea-agent", "timeout")
	scrapes := series(t, m, "antrea_ui_metrics_tap_scrapes_total", "target_group", "outcome")
	require.Len(t, scrapes, 2)
	assert.Equal(t, 2.0, scrapes["antrea-agent success"].GetCounter().GetValue())
	assert.Equal(t, 1.0, scrapes["antrea-agent timeout"].GetCounter().GetValue())
}
