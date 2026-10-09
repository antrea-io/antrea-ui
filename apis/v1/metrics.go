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

package v1

import "time"

// Types for the live metrics tap API (/api/v1/metrics). See docs/metrics.md.

// MetricsTarget is one scrapable member of a target group.
type MetricsTarget struct {
	// ID is what a client names in a selection: the name of the target group when it holds a
	// single target, "<target group>/<instance>" otherwise (for example "antrea-agent/<node>").
	ID     string            `json:"id"`
	Labels map[string]string `json:"labels,omitempty"`
}

// MetricsTargetGroup is a set of targets which are discovered and authorized together (the Antrea
// Controller, the Antrea Agents, ...), along with the targets it currently has.
type MetricsTargetGroup struct {
	Name string `json:"name"`
	// Allowed reports whether the caller may read metrics from this target group. Targets is empty
	// when it is false: nothing is discovered for a target group the caller cannot read.
	Allowed bool `json:"allowed"`
	// Available is false when the target group cannot be scraped at the moment, with Reason saying
	// why. It describes the target group, not the caller.
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// Targets is never null.
	Targets []MetricsTarget `json:"targets"`
}

// MetricsTargetList is the response of GET /api/v1/metrics/targets.
type MetricsTargetList struct {
	TargetGroups []MetricsTargetGroup `json:"targetGroups"`
}

// MetricFamilyInfo describes one metric family exposed by a target, without its sample values.
type MetricFamilyInfo struct {
	Name string `json:"name"`
	// Type is the Prometheus metric type in lower case: counter, gauge, histogram, summary or
	// untyped.
	Type string `json:"type"`
	Help string `json:"help"`
	// LabelNames is the sorted union of the label names used by the series of the family.
	LabelNames []string `json:"labelNames"`
	// SeriesCount is the number of label sets of the family as exposed: a histogram counts
	// once per label set, not once per bucket.
	SeriesCount int `json:"seriesCount"`
}

// MetricFamilyList is the response of GET /api/v1/metrics/families.
type MetricFamilyList struct {
	Target string `json:"target"`
	// Timestamp is when the target was scraped.
	Timestamp time.Time          `json:"timestamp"`
	Families  []MetricFamilyInfo `json:"families"`
}

// MetricsSelection names the metric families a tap reads from one target.
type MetricsSelection struct {
	Target  string   `json:"target"`
	Metrics []string `json:"metrics"`
}

// MetricsTapRequest is the body of POST /api/v1/metrics/taps.
type MetricsTapRequest struct {
	// Interval is a Go duration string ("10s"). Optional.
	Interval string `json:"interval,omitempty"`
	// Selections names at least one target.
	Selections []MetricsSelection `json:"selections"`
}

// MetricsTapSelectionsRequest is the body of PUT /api/v1/metrics/taps/:id/selections. It replaces
// the whole selection of the tap.
type MetricsTapSelectionsRequest struct {
	Selections []MetricsSelection `json:"selections"`
}

// MetricsTapEvent is the JSON payload of an SSE "tap" event. It is the first event of a stream
// and is sent again after each accepted selection update.
type MetricsTapEvent struct {
	ID         string             `json:"id"`
	Interval   string             `json:"interval"`
	Selections []MetricsSelection `json:"selections"`
}

// MetricSample is one sample of a metric family.
type MetricSample struct {
	// Name is only set when it differs from the name of the family, which is the case for the
	// _bucket, _sum and _count series of histograms and summaries.
	Name   string            `json:"name,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
	// Value is a string, as in the Prometheus HTTP API, because NaN and +Inf are ordinary
	// values (summary quantiles, histogram buckets) which JSON numbers cannot carry.
	Value string `json:"value"`
}

// MetricFamilySamples holds the samples of one metric family from one scrape.
type MetricFamilySamples struct {
	Name    string         `json:"name"`
	Type    string         `json:"type"`
	Samples []MetricSample `json:"samples"`
}

// MetricsScrapeEvent is the JSON payload of an SSE "scrape" event: the selected families of one
// target, from one scrape.
type MetricsScrapeEvent struct {
	Target string `json:"target"`
	// Timestamp is when the scrape was actually performed.
	Timestamp time.Time             `json:"timestamp"`
	Families  []MetricFamilySamples `json:"families"`
	// Truncated is set when the event hit the cap on samples per event and some were left out.
	Truncated bool `json:"truncated,omitempty"`
}

// MetricsScrapeErrorEvent is the JSON payload of an SSE "scrape_error" event. It is not terminal:
// the tap keeps scraping the target on the following ticks.
type MetricsScrapeErrorEvent struct {
	Target    string    `json:"target"`
	Timestamp time.Time `json:"timestamp"`
	// Code is a stable, machine-readable identifier of the failure class.
	Code    string `json:"code"`
	Message string `json:"message"`
}

// MetricsTapErrorEvent is the JSON payload of an SSE "error" event, which ends the stream. It has
// the same shape as FlowStreamErrorEvent.
type MetricsTapErrorEvent struct {
	Message   string `json:"message"`
	Code      string `json:"code,omitempty"`
	Retryable bool   `json:"retryable"`
}
