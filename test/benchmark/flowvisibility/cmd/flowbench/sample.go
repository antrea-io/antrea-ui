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
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"

	"antrea.io/antrea-ui/test/benchmark/flowvisibility/fakefa"
)

// backendMetrics is what is read from the backend's own /metrics: its process, not its Pod, so
// the frontend's nginx container is not included.
type backendMetrics struct {
	CPUSeconds float64
	RSSBytes   float64
	HeapInuse  float64
	Goroutines float64
}

func scrapeBackend(ctx context.Context, url, token string) (backendMetrics, error) {
	var m backendMetrics
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/metrics", nil)
	if err != nil {
		return m, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return m, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return m, fmt.Errorf("GET /metrics: HTTP %d: %s", resp.StatusCode, body)
	}
	want := map[string]*float64{
		"process_cpu_seconds_total":     &m.CPUSeconds,
		"process_resident_memory_bytes": &m.RSSBytes,
		"go_memstats_heap_inuse_bytes":  &m.HeapInuse,
		"go_goroutines":                 &m.Goroutines,
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		name, value, ok := strings.Cut(sc.Text(), " ")
		if !ok {
			continue
		}
		if dst, ok := want[name]; ok {
			*dst, _ = strconv.ParseFloat(strings.TrimSpace(value), 64)
		}
	}
	return m, sc.Err()
}

// fakeFAStats reads the fake Flow Aggregator's /stats through the API server's Service proxy.
func fakeFAStats(ctx context.Context, cs kubernetes.Interface) (fakefa.Stats, error) {
	var st fakefa.Stats
	data, err := cs.CoreV1().Services(faNamespace).ProxyGet("http", faName, strconv.Itoa(faStatsPort), "/stats", nil).DoRaw(ctx)
	if err != nil {
		return st, err
	}
	err = json.Unmarshal(data, &st)
	return st, err
}

// procUsage is the CPU time and memory of a set of processes.
type procUsage struct {
	CPUSeconds float64
	RSSBytes   float64
	Count      int
}

// clockTicks is USER_HZ, which is 100 on every Linux architecture flowbench runs on.
const clockTicks = 100

// browserUsage sums the usage of root's descendants, by Chromium process type: "browser" for the
// main process, then "renderer", "gpu-process", "utility" and so on, from their --type flag.
func browserUsage(root int) (map[string]procUsage, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	type proc struct {
		ppid     int
		cpu, rss float64
	}
	procs := map[int]proc{}
	pageSize := float64(os.Getpagesize())
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			continue
		}
		// The command name is in parentheses and may contain spaces: fields are counted from
		// after its closing one.
		_, rest, ok := strings.CutLast(string(stat), ")")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 22 {
			continue
		}
		ppid, _ := strconv.Atoi(f[1])
		utime, _ := strconv.ParseFloat(f[11], 64)
		stime, _ := strconv.ParseFloat(f[12], 64)
		rssPages, _ := strconv.ParseFloat(f[21], 64)
		procs[pid] = proc{ppid: ppid, cpu: (utime + stime) / clockTicks, rss: rssPages * pageSize}
	}
	children := map[int][]int{}
	for pid, p := range procs {
		children[p.ppid] = append(children[p.ppid], pid)
	}
	usage := map[string]procUsage{}
	var walk func(pid int)
	walk = func(pid int) {
		for _, c := range children[pid] {
			typ := chromiumType(c)
			if typ != "" {
				u := usage[typ]
				u.CPUSeconds += procs[c].cpu
				u.RSSBytes += procs[c].rss
				u.Count++
				usage[typ] = u
			}
			walk(c)
		}
	}
	walk(root)
	return usage, nil
}

// chromiumType returns the Chromium process type of pid, or "" for a process that is not part of
// Chromium (the Playwright driver itself, for example).
func chromiumType(pid int) string {
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return ""
	}
	// Chromium rewrites its process title, which joins its arguments with spaces rather than
	// NULs: split on both.
	args := strings.Fields(strings.ReplaceAll(string(cmdline), "\x00", " "))
	if len(args) == 0 || !strings.Contains(args[0], "chrom") {
		return ""
	}
	for _, a := range args[1:] {
		if t, ok := strings.CutPrefix(a, "--type="); ok {
			return t
		}
	}
	return "browser"
}

// hostSample is one sample of everything flowbench measures from outside the page.
type hostSample struct {
	T       time.Time            `json:"t"`
	Backend *backendMetrics      `json:"backend,omitempty"`
	FakeFA  *fakefa.Stats        `json:"fakefa,omitempty"`
	Browser map[string]procUsage `json:"browser,omitempty"`
	Errors  []string             `json:"errors,omitempty"`
}

type sampler struct {
	url         string
	token       string
	clientset   kubernetes.Interface
	browserRoot int // 0 when there is no browser
}

func (s *sampler) sample(ctx context.Context) hostSample {
	hs := hostSample{T: time.Now()}
	if m, err := scrapeBackend(ctx, s.url, s.token); err != nil {
		hs.Errors = append(hs.Errors, "backend: "+err.Error())
	} else {
		hs.Backend = &m
	}
	if st, err := fakeFAStats(ctx, s.clientset); err != nil {
		hs.Errors = append(hs.Errors, "fakefa: "+err.Error())
	} else {
		st.Summary, st.Scale = "", nil
		hs.FakeFA = &st
	}
	if s.browserRoot != 0 {
		if u, err := browserUsage(s.browserRoot); err != nil {
			hs.Errors = append(hs.Errors, "browser: "+err.Error())
		} else {
			hs.Browser = u
		}
	}
	return hs
}
