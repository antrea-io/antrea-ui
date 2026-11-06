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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/util/wait"
)

type runOptions struct {
	deployOptions
	scaleFlags
	noDeploy       bool
	url            string
	path           string
	warmup         time.Duration
	duration       time.Duration
	sampleInterval time.Duration
	resultsDir     string
	traceSeconds   int
	width, height  int
	nodeBin        string
}

func runRun(ctx context.Context, args []string) error {
	fs := pflag.NewFlagSet("run", pflag.ExitOnError)
	o := &runOptions{deployOptions: deployOptions{repoRoot: repoRoot()}}
	o.cluster.addFlags(fs)
	addDeployFlags(fs, &o.deployOptions)
	o.addFlags(fs, true)
	fs.BoolVar(&o.noDeploy, "no-deploy", false, "measure what is deployed as it is, ignoring --scale and --set")
	fs.StringVar(&o.url, "url", "", "URL antrea-ui is reached at (defaults to the first Node's address and the NodePort)")
	fs.StringVar(&o.path, "path", "/flows/map?namespace=_all", "page to measure: the service map by default, /flows/list?namespace=_all for the flow list")
	fs.DurationVar(&o.warmup, "warmup", 30*time.Second, "time between the first render and the start of the measurement")
	fs.DurationVar(&o.duration, "duration", 2*time.Minute, "length of the measurement")
	fs.DurationVar(&o.sampleInterval, "sample-interval", 2*time.Second, "interval between samples")
	fs.StringVar(&o.resultsDir, "results", filepath.Join(o.repoRoot, "test/benchmark/flowvisibility/results"), "directory results are written under")
	fs.IntVar(&o.traceSeconds, "trace-seconds", 0, "also record a Chrome performance trace of the first N seconds of the measurement (open it in DevTools' Performance panel)")
	fs.IntVar(&o.width, "width", 1600, "browser viewport width")
	fs.IntVar(&o.height, "height", 1000, "browser viewport height")
	fs.StringVar(&o.nodeBin, "node", "node", "Node.js binary")
	_ = fs.Parse(args)

	if err := o.connect(); err != nil {
		return err
	}
	names, scales, err := o.resolve()
	if err != nil {
		return err
	}
	if o.noDeploy {
		names, scales = []string{"as-deployed"}, scales[:1]
	}
	runDir := filepath.Join(o.resultsDir, time.Now().Format("20060102-150405"))
	var summaries []*summary
	for i, name := range names {
		if !o.noDeploy {
			logf("Deploying scale %s", name)
			o.scale = scales[i]
			if err := deploy(ctx, &o.deployOptions); err != nil {
				return err
			}
		}
		dir := filepath.Join(runDir, strings.NewReplacer("/", "_", "=", "-", ",", "_").Replace(name))
		s, err := measure(ctx, o, name, dir)
		if err != nil {
			return fmt.Errorf("scale %s: %w", name, err)
		}
		summaries = append(summaries, s)
		fmt.Println(s.markdown())
	}
	if len(summaries) > 1 {
		table := comparisonMarkdown(summaries)
		if err := os.WriteFile(filepath.Join(runDir, "comparison.md"), []byte(table), 0o600); err != nil {
			return err
		}
		fmt.Println(table)
	}
	logf("Results are in %s", runDir)
	return nil
}

// browserEvent is one line of browser/run.mjs's output.
type browserEvent struct {
	Event   string  `json:"event"`
	T       int64   `json:"t"`
	MS      float64 `json:"ms"`
	Message string  `json:"message"`
	Path    string  `json:"path"`
	Phase   string  `json:"phase"`
}

func measure(ctx context.Context, o *runOptions, name, dir string) (*summary, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	cs := o.clientset
	url := o.url
	var err error
	if url == "" {
		if url, err = defaultURL(ctx, cs, o.nodePort); err != nil {
			return nil, err
		}
	}
	token, err := benchToken(ctx, cs)
	if err != nil {
		return nil, err
	}
	if err := waitForUI(ctx, url); err != nil {
		return nil, err
	}
	st, err := fakeFAStats(ctx, cs)
	if err != nil {
		return nil, fmt.Errorf("failed to read the fake Flow Aggregator's stats: %w", err)
	}
	logf("Synthetic cluster: %s", st.Summary)
	if err := writeJSON(filepath.Join(dir, "scale.json"), st.Scale); err != nil {
		return nil, err
	}

	cfg := map[string]any{
		"url":                   url,
		"token":                 token,
		"path":                  o.path,
		"warmupSeconds":         o.warmup.Seconds(),
		"durationSeconds":       o.duration.Seconds(),
		"sampleIntervalSeconds": o.sampleInterval.Seconds(),
		"width":                 o.width,
		"height":                o.height,
	}
	if o.traceSeconds > 0 {
		cfg["traceFile"] = filepath.Join(dir, "trace.json")
		cfg["traceSeconds"] = o.traceSeconds
	}
	cfgJSON, _ := json.Marshal(cfg)
	browserDir := filepath.Join(o.repoRoot, "test/benchmark/flowvisibility/browser")
	// #nosec G204: the Node.js binary named by --node, running the script next to flowbench
	cmd := exec.CommandContext(ctx, o.nodeBin, filepath.Join(browserDir, "run.mjs"))
	cmd.Dir = browserDir
	cmd.Env = append(os.Environ(), "FLOWBENCH_CONFIG="+string(cfgJSON))
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	pageSamples, err := os.Create(filepath.Join(dir, "page-samples.jsonl"))
	if err != nil {
		return nil, err
	}
	defer pageSamples.Close()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start the browser (did you run npm ci in %s?): %w", browserDir, err)
	}
	logf("Browser started: warmup %s, then measuring for %s", o.warmup, o.duration)

	s := &sampler{url: url, token: token, clientset: cs, browserRoot: cmd.Process.Pid}
	var mu sync.Mutex
	var hostSamples []hostSample
	measuring := false
	samplerCtx, stopSampler := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() {
		ticker := time.NewTicker(o.sampleInterval)
		defer ticker.Stop()
		for {
			hs := s.sample(samplerCtx)
			mu.Lock()
			if measuring {
				hostSamples = append(hostSamples, hs)
			}
			mu.Unlock()
			select {
			case <-samplerCtx.Done():
				return
			case <-ticker.C:
			}
		}
	})

	sum := &summary{Name: name, Scale: st.Scale, Cluster: st.Summary}
	var pageLines []json.RawMessage
	var fatal string
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := append([]byte(nil), sc.Bytes()...)
		var ev browserEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			logf("browser: %s", line)
			continue
		}
		switch ev.Event {
		case "first-render":
			sum.FirstRenderSeconds = ev.MS / 1000
			logf("First render after %.1fs", sum.FirstRenderSeconds)
		case "measure-start":
			mu.Lock()
			measuring = true
			mu.Unlock()
			logf("Measuring")
		case "done":
			mu.Lock()
			measuring = false
			mu.Unlock()
		case "settled":
			settle := ev.MS / 1000
			sum.SettleSeconds = &settle
			logf("Settled %.1fs after the first render", settle)
		case "sample":
			// Warmup samples are kept in the file, but only the measurement is summarized.
			if ev.Phase == "measure" {
				pageLines = append(pageLines, line)
			}
			_, _ = pageSamples.Write(append(line, '\n'))
		case "page-error", "console-error":
			sum.PageErrors = append(sum.PageErrors, ev.Message)
			logf("browser %s: %s", ev.Event, ev.Message)
		case "trace-written":
			logf("Trace written to %s", ev.Path)
		case "fatal":
			fatal = ev.Message
		}
	}
	waitErr := cmd.Wait()
	stopSampler()
	wg.Wait()
	if fatal != "" {
		return nil, fmt.Errorf("browser failed: %s", fatal)
	}
	if waitErr != nil {
		return nil, fmt.Errorf("browser failed: %w", waitErr)
	}

	if err := writeJSONL(filepath.Join(dir, "host-samples.jsonl"), hostSamples); err != nil {
		return nil, err
	}
	sum.compute(hostSamples, pageLines)
	if err := writeJSON(filepath.Join(dir, "summary.json"), sum); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte(sum.markdown()), 0o600); err != nil {
		return nil, err
	}
	return sum, nil
}

func runMonitor(ctx context.Context, args []string) error {
	fs := pflag.NewFlagSet("monitor", pflag.ExitOnError)
	var c cluster
	c.addFlags(fs)
	var url string
	var nodePort int
	var interval time.Duration
	fs.StringVar(&url, "url", "", "URL antrea-ui is reached at (defaults to the first Node's address and the NodePort)")
	fs.IntVar(&nodePort, "node-port", 31234, "NodePort of the antrea-ui Service")
	fs.DurationVar(&interval, "interval", 5*time.Second, "interval between samples")
	_ = fs.Parse(args)
	if err := c.connect(); err != nil {
		return err
	}
	var err error
	if url == "" {
		if url, err = defaultURL(ctx, c.clientset, nodePort); err != nil {
			return err
		}
	}
	token, err := benchToken(ctx, c.clientset)
	if err != nil {
		return err
	}
	s := &sampler{url: url, token: token, clientset: c.clientset}
	prev := s.sample(ctx)
	fmt.Printf("%-8s %12s %12s %10s %8s %8s %12s\n", "time", "backend cpu", "backend rss", "heap", "gorout.", "streams", "flows/s")
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		cur := s.sample(ctx)
		for _, e := range cur.Errors {
			logf("%s", e)
		}
		// A counter that went backwards is a restart, of the backend or of fakefa (a new scale):
		// that line is skipped.
		if cur.Backend != nil && prev.Backend != nil && cur.FakeFA != nil && prev.FakeFA != nil &&
			cur.Backend.CPUSeconds >= prev.Backend.CPUSeconds && cur.FakeFA.RecordsSent >= prev.FakeFA.RecordsSent {
			dt := cur.T.Sub(prev.T).Seconds()
			fmt.Printf("%-8s %11.1f%% %9.1fMiB %7.1fMiB %8.0f %8d %12.1f\n", cur.T.Format("15:04:05"),
				100*(cur.Backend.CPUSeconds-prev.Backend.CPUSeconds)/dt, cur.Backend.RSSBytes/mib, cur.Backend.HeapInuse/mib,
				cur.Backend.Goroutines, cur.FakeFA.StreamsOpen, float64(cur.FakeFA.RecordsSent-prev.FakeFA.RecordsSent)/dt)
		}
		prev = cur
	}
}

func waitForUI(ctx context.Context, url string) error {
	return wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/v1/version", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false, nil
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK, nil
	})
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func writeJSONL[T any](path string, items []T) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, it := range items {
		if err := enc.Encode(it); err != nil {
			return err
		}
	}
	return nil
}
