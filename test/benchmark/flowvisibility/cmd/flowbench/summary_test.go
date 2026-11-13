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

package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The mean of a rate is over the whole measurement: a long, busy interval counts for more than a
// short, idle one.
func TestSummaryRateMeansWeighIntervals(t *testing.T) {
	pageLines := []json.RawMessage{
		json.RawMessage(`{"dt": 2, "pageDt": 2.5, "mainThreadBusy": 0, "fps": 60, "jsHeapUsed": 1048576}`),
		json.RawMessage(`{"dt": 8, "pageDt": 7.5, "mainThreadBusy": 1, "fps": 20, "jsHeapUsed": 3145728}`),
	}
	t0 := time.Unix(1_800_000_000, 0)
	host := []hostSample{
		{T: t0, Backend: &backendMetrics{CPUSeconds: 0}},
		{T: t0.Add(2 * time.Second), Backend: &backendMetrics{CPUSeconds: 0}},
		{T: t0.Add(10 * time.Second), Backend: &backendMetrics{CPUSeconds: 8}},
	}
	var s summary
	s.compute(host, pageLines)

	assert.InDelta(t, 80, s.Metrics["mainThreadBusy"].Mean, 1e-9, "busy 8s out of 10s")
	assert.InDelta(t, 30, s.Metrics["fps"].Mean, 1e-9, "300 frames in 10s")
	assert.InDelta(t, 80, s.Metrics["backendCPU"].Mean, 1e-9, "8 CPU seconds in 10s")
	assert.InDelta(t, 2, s.Metrics["jsHeapUsedMiB"].Mean, 1e-9, "a gauge's samples weigh the same")
	assert.InDelta(t, 100, s.Metrics["mainThreadBusy"].Max, 1e-9, "the maximum is a sample's")
}
