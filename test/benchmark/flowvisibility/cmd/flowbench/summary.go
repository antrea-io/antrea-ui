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
	"fmt"
	"math"
	"slices"
	"strings"

	"antrea.io/antrea-ui/test/benchmark/flowvisibility/fakefa"
)

const mib = 1024 * 1024

// stat summarizes one metric over the measurement.
type stat struct {
	Mean float64 `json:"mean"`
	P50  float64 `json:"p50"`
	P95  float64 `json:"p95"`
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
}

func newStat(values []float64) stat {
	if len(values) == 0 {
		return stat{Mean: math.NaN(), P50: math.NaN(), P95: math.NaN(), Min: math.NaN(), Max: math.NaN()}
	}
	v := slices.Clone(values)
	slices.Sort(v)
	sum := 0.0
	for _, x := range v {
		sum += x
	}
	pct := func(p float64) float64 { return v[min(len(v)-1, int(math.Ceil(p*float64(len(v))))-1)] }
	return stat{Mean: sum / float64(len(v)), P50: pct(0.5), P95: pct(0.95), Min: v[0], Max: v[len(v)-1]}
}

type summary struct {
	Name               string        `json:"name"`
	Cluster            string        `json:"cluster"`
	Scale              *fakefa.Scale `json:"scale"`
	FirstRenderSeconds float64       `json:"firstRenderSeconds"`
	// SettleSeconds is how long after the first render the renderer's main thread first went
	// under 20% busy, or nil if it did not during the warmup.
	SettleSeconds *float64 `json:"settleSeconds"`
	PageErrors    []string `json:"pageErrors,omitempty"`
	// Metrics maps a metric name (see metricOrder) to its summary over the measurement.
	Metrics map[string]stat `json:"metrics"`
}

// metricDef says how a metric is shown: its label, unit and which of its stats matter.
type metricDef struct {
	key, label, format string
	stats              []string // "mean", "p95", "max", ...
}

var metricOrder = []metricDef{
	{"flowsPerSec", "Flows streamed to antrea-ui (records/s)", "%.1f", []string{"mean"}},
	{"streamMBps", "Flow stream from the fake FA (MB/s, protobuf)", "%.3f", []string{"mean"}},
	{"storeEntries", "Connections in the page's flow store", "%.0f", []string{"max"}},
	{"mapNodes", "Service map nodes", "%.0f", []string{"max"}},
	{"mapEdges", "Service map edges", "%.0f", []string{"max"}},
	{"svgElements", "SVG elements", "%.0f", []string{"max"}},
	{"mainThreadBusy", "Renderer main thread busy (%)", "%.1f", []string{"mean", "p95"}},
	{"scriptBusy", "  of which script (%)", "%.1f", []string{"mean"}},
	{"layoutBusy", "  of which layout (%)", "%.1f", []string{"mean"}},
	{"styleBusy", "  of which style recalc (%)", "%.1f", []string{"mean"}},
	{"fps", "Frames per second", "%.1f", []string{"mean", "min"}},
	{"maxFrameGapMs", "Longest frame gap (ms)", "%.0f", []string{"p95", "max"}},
	{"longTasksPerSec", "Long tasks (>50ms) per second", "%.2f", []string{"mean"}},
	{"longTaskMaxMs", "Longest long task (ms)", "%.0f", []string{"max"}},
	{"maxLoopLagMs", "Event loop lag (ms)", "%.0f", []string{"p95", "max"}},
	{"jsHeapUsedMiB", "JS heap used (MiB)", "%.1f", []string{"mean", "max"}},
	{"domNodes", "DOM nodes", "%.0f", []string{"max"}},
	{"rendererCPU", "Browser renderer CPU (%)", "%.1f", []string{"mean", "p95"}},
	{"gpuCPU", "Browser GPU process CPU (%)", "%.1f", []string{"mean", "p95"}},
	{"browserCPU", "Browser total CPU (%)", "%.1f", []string{"mean", "p95"}},
	{"browserRSSMiB", "Browser total RSS (MiB)", "%.0f", []string{"max"}},
	{"backendCPU", "Backend CPU (%)", "%.1f", []string{"mean", "p95"}},
	{"backendRSSMiB", "Backend RSS (MiB)", "%.1f", []string{"mean", "max"}},
	{"backendHeapMiB", "Backend Go heap in use (MiB)", "%.1f", []string{"mean", "max"}},
	{"backendGoroutines", "Backend goroutines", "%.0f", []string{"max"}},
}

// pageSample is the subset of a browser/run.mjs sample the summary uses.
type pageSample struct {
	DT             float64  `json:"dt"`
	MainThreadBusy float64  `json:"mainThreadBusy"`
	ScriptBusy     float64  `json:"scriptBusy"`
	LayoutBusy     float64  `json:"layoutBusy"`
	StyleBusy      float64  `json:"styleBusy"`
	JSHeapUsed     float64  `json:"jsHeapUsed"`
	DOMNodes       float64  `json:"domNodes"`
	FPS            float64  `json:"fps"`
	LongTaskCount  float64  `json:"longTaskCount"`
	LongTaskMaxMs  float64  `json:"longTaskMaxMs"`
	MaxFrameGapMs  float64  `json:"maxFrameGapMs"`
	MaxLoopLagMs   float64  `json:"maxLoopLagMs"`
	MapNodes       float64  `json:"mapNodes"`
	MapEdges       float64  `json:"mapEdges"`
	SVGElements    float64  `json:"svgElements"`
	StoreEntries   *float64 `json:"storeEntries"`
}

func (s *summary) compute(host []hostSample, pageLines []json.RawMessage) {
	series := map[string][]float64{}
	add := func(k string, v float64) { series[k] = append(series[k], v) }

	for _, line := range pageLines {
		var p pageSample
		if err := json.Unmarshal(line, &p); err != nil {
			continue
		}
		add("mainThreadBusy", 100*p.MainThreadBusy)
		add("scriptBusy", 100*p.ScriptBusy)
		add("layoutBusy", 100*p.LayoutBusy)
		add("styleBusy", 100*p.StyleBusy)
		add("jsHeapUsedMiB", p.JSHeapUsed/mib)
		add("domNodes", p.DOMNodes)
		add("fps", p.FPS)
		add("longTasksPerSec", p.LongTaskCount/p.DT)
		add("longTaskMaxMs", p.LongTaskMaxMs)
		add("maxFrameGapMs", p.MaxFrameGapMs)
		add("maxLoopLagMs", p.MaxLoopLagMs)
		add("mapNodes", p.MapNodes)
		add("mapEdges", p.MapEdges)
		add("svgElements", p.SVGElements)
		if p.StoreEntries != nil {
			add("storeEntries", *p.StoreEntries)
		}
	}

	for i := 1; i < len(host); i++ {
		prev, cur := host[i-1], host[i]
		dt := cur.T.Sub(prev.T).Seconds()
		if dt <= 0 {
			continue
		}
		if prev.Backend != nil && cur.Backend != nil {
			add("backendCPU", 100*(cur.Backend.CPUSeconds-prev.Backend.CPUSeconds)/dt)
			add("backendRSSMiB", cur.Backend.RSSBytes/mib)
			add("backendHeapMiB", cur.Backend.HeapInuse/mib)
			add("backendGoroutines", cur.Backend.Goroutines)
		}
		if prev.FakeFA != nil && cur.FakeFA != nil {
			add("flowsPerSec", float64(cur.FakeFA.RecordsSent-prev.FakeFA.RecordsSent)/dt)
			add("streamMBps", float64(cur.FakeFA.BytesSent-prev.FakeFA.BytesSent)/dt/1e6)
		}
		if prev.Browser != nil && cur.Browser != nil {
			total, totalRSS := 0.0, 0.0
			for typ, u := range cur.Browser {
				p := prev.Browser[typ]
				cpu := 100 * (u.CPUSeconds - p.CPUSeconds) / dt
				// A process that exited between samples makes the sum go backwards.
				cpu = max(cpu, 0)
				switch typ {
				case "renderer":
					add("rendererCPU", cpu)
				case "gpu-process":
					add("gpuCPU", cpu)
				}
				total += cpu
				totalRSS += u.RSSBytes
			}
			add("browserCPU", total)
			add("browserRSSMiB", totalRSS/mib)
		}
	}

	s.Metrics = map[string]stat{}
	for k, v := range series {
		s.Metrics[k] = newStat(v)
	}
}

func (st stat) get(name string) float64 {
	switch name {
	case "mean":
		return st.Mean
	case "p50":
		return st.P50
	case "p95":
		return st.P95
	case "min":
		return st.Min
	default:
		return st.Max
	}
}

func (s *summary) cell(def metricDef) string {
	st, ok := s.Metrics[def.key]
	if !ok {
		return "-"
	}
	parts := make([]string, 0, len(def.stats))
	for _, name := range def.stats {
		parts = append(parts, fmt.Sprintf(def.format, st.get(name)))
	}
	return strings.Join(parts, " / ")
}

func statsLabel(def metricDef) string {
	return def.label + " [" + strings.Join(def.stats, " / ") + "]"
}

func (s *summary) markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n%s\n\nFirst render of the page: %.1fs. Settled (main thread under 20%% busy): %s.\n\n",
		s.Name, s.Cluster, s.FirstRenderSeconds, s.settle())
	b.WriteString("| Metric | Value |\n| --- | --- |\n")
	for _, def := range metricOrder {
		fmt.Fprintf(&b, "| %s | %s |\n", statsLabel(def), s.cell(def))
	}
	if len(s.PageErrors) > 0 {
		fmt.Fprintf(&b, "\n%d page errors, first: %s\n", len(s.PageErrors), s.PageErrors[0])
	}
	return b.String()
}

func comparisonMarkdown(sums []*summary) string {
	var b strings.Builder
	b.WriteString("## Comparison\n\n| Metric |")
	for _, s := range sums {
		fmt.Fprintf(&b, " %s |", s.Name)
	}
	b.WriteString("\n| --- |")
	for range sums {
		b.WriteString(" --- |")
	}
	b.WriteString("\n| First render (s) |")
	for _, s := range sums {
		fmt.Fprintf(&b, " %.1f |", s.FirstRenderSeconds)
	}
	b.WriteString("\n| Settled after first render (s) |")
	for _, s := range sums {
		fmt.Fprintf(&b, " %s |", s.settle())
	}
	b.WriteString("\n")
	for _, def := range metricOrder {
		fmt.Fprintf(&b, "| %s |", statsLabel(def))
		for _, s := range sums {
			fmt.Fprintf(&b, " %s |", s.cell(def))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (s *summary) settle() string {
	if s.SettleSeconds == nil {
		return "not during the warmup"
	}
	return fmt.Sprintf("%.1fs", *s.SettleSeconds)
}
