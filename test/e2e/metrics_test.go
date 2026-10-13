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

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

const (
	metricsControllerTarget = "antrea-controller"
	metricsSelfTarget       = "antrea-ui"
	// metricsTapInterval is the shortest interval the default configuration accepts.
	metricsTapInterval = "5s"
	// metricsStreamTimeout bounds a test which reads a tap stream: a tap which stops sending
	// what the test waits for still sends keepalives, so reads alone would never fail.
	metricsStreamTimeout = 2 * time.Minute
)

func skipIfMetricsDisabled(t *testing.T) {
	t.Helper()
	if !settings.Features.MetricsEnabled {
		t.Skip("Metrics are not enabled in this deployment")
	}
}

// sseEvent is one event of an SSE stream.
type sseEvent struct {
	Name string
	Data string
}

// sseReader reads the events of an SSE stream, skipping comments such as keepalives.
type sseReader struct {
	scanner *bufio.Scanner
}

func newSSEReader(r io.Reader) *sseReader {
	scanner := bufio.NewScanner(r)
	// One event is one line of data, which for a scrape can be far longer than the default
	// limit of the scanner.
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	return &sseReader{scanner: scanner}
}

// Next returns the next event of the stream. It returns io.EOF when the stream ends, and the
// error of the request when its context does.
func (r *sseReader) Next() (sseEvent, error) {
	var event sseEvent
	for r.scanner.Scan() {
		line := r.scanner.Text()
		switch {
		case line == "":
			if event != (sseEvent{}) {
				return event, nil
			}
		case strings.HasPrefix(line, ":"):
			// comment
		case strings.HasPrefix(line, "event:"):
			event.Name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			event.Data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	if err := r.scanner.Err(); err != nil {
		return sseEvent{}, err
	}
	return sseEvent{}, io.EOF
}

func metricsRequest(ctx context.Context, token, method, path string, query url.Values, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	u := &url.URL{Scheme: "http", Host: host, Path: path, RawQuery: query.Encode()}
	return RequestURLWithClient(ctx, http.DefaultClient, method, u, reader, setAccessTokenMutator(token))
}

// metricsStatus performs a request and returns the status of the response. It never fails the
// test itself, so that it can be used from a require.Eventually condition.
//
// The body is closed without being read: when the request opens a tap, the body is a stream which
// does not end, and closing it is what ends the tap.
func metricsStatus(ctx context.Context, token, method, path string, query url.Values, body any) (int, error) {
	resp, err := metricsRequest(ctx, token, method, path, query, body)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

func getMetricsTargets(t *testing.T, token string) map[string]apisv1.MetricsTargetGroup {
	t.Helper()
	resp, err := metricsRequest(t.Context(), token, "GET", "api/v1/metrics/targets", nil, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var list apisv1.MetricsTargetList
	require.NoError(t, getResponseBody(resp, &list))
	targetGroups := make(map[string]apisv1.MetricsTargetGroup, len(list.TargetGroups))
	for _, targetGroup := range list.TargetGroups {
		targetGroups[targetGroup.Name] = targetGroup
	}
	return targetGroups
}

func getMetricFamilies(t *testing.T, token, target string) apisv1.MetricFamilyList {
	t.Helper()
	resp, err := metricsRequest(t.Context(), token, "GET", "api/v1/metrics/families", url.Values{"target": {target}}, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "target %s", target)
	var list apisv1.MetricFamilyList
	require.NoError(t, getResponseBody(resp, &list))
	return list
}

// firstFamilyWithPrefix returns the name of a gauge or counter family of the target whose name
// starts with prefix: a family which yields samples under its own name.
func firstFamilyWithPrefix(t *testing.T, list apisv1.MetricFamilyList, prefix string) string {
	t.Helper()
	for _, family := range list.Families {
		if strings.HasPrefix(family.Name, prefix) && (family.Type == "gauge" || family.Type == "counter") && family.SeriesCount > 0 {
			return family.Name
		}
	}
	require.FailNowf(t, "no such family", "target %s exposes no gauge or counter starting with %q", list.Target, prefix)
	return ""
}

// firstAgentTarget returns the ID of the first agent target, which the caller of token must be
// allowed to list.
func firstAgentTarget(t *testing.T, token string) string {
	t.Helper()
	agents := getMetricsTargets(t, token)["antrea-agent"]
	require.NotEmpty(t, agents.Targets, "no Antrea agent target: %+v", agents)
	return agents.Targets[0].ID
}

func TestMetricsTargets(t *testing.T) {
	skipIfMetricsDisabled(t)
	ctx := t.Context()
	token, err := GetAccessToken(ctx, host)
	require.NoError(t, err)

	targetGroups := getMetricsTargets(t, token)

	controller := targetGroups["antrea-controller"]
	assert.True(t, controller.Allowed)
	assert.True(t, controller.Available, controller.Reason)
	assert.Equal(t, []apisv1.MetricsTarget{{ID: metricsControllerTarget}}, controller.Targets)

	self := targetGroups["antrea-ui"]
	assert.True(t, self.Allowed)
	assert.True(t, self.Available, self.Reason)
	assert.Equal(t, []apisv1.MetricsTarget{{ID: metricsSelfTarget}}, self.Targets)

	// One agent per Node: the antrea-agent DaemonSet tolerates every taint.
	nodes, err := k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	var expected []apisv1.MetricsTarget
	for _, node := range nodes.Items {
		expected = append(expected, apisv1.MetricsTarget{ID: "antrea-agent/" + node.Name, Labels: map[string]string{"node": node.Name}})
	}
	agents := targetGroups["antrea-agent"]
	assert.True(t, agents.Allowed)
	assert.True(t, agents.Available, agents.Reason)
	assert.ElementsMatch(t, expected, agents.Targets)

	flowAggregator, ok := targetGroups["flow-aggregator"]
	require.True(t, ok)
	assert.False(t, flowAggregator.Available)
	assert.NotEmpty(t, flowAggregator.Reason)
	assert.Empty(t, flowAggregator.Targets)
}

func TestMetricFamilies(t *testing.T) {
	skipIfMetricsDisabled(t)
	token, err := GetAccessToken(t.Context(), host)
	require.NoError(t, err)

	for target, prefix := range map[string]string{
		metricsControllerTarget:    "antrea_controller_",
		firstAgentTarget(t, token): "antrea_agent_",
		metricsSelfTarget:          "antrea_ui_",
	} {
		t.Run(target, func(t *testing.T) {
			list := getMetricFamilies(t, token, target)
			assert.Equal(t, target, list.Target)
			assert.WithinDuration(t, time.Now(), list.Timestamp, time.Minute)
			firstFamilyWithPrefix(t, list, prefix)
			for i := 1; i < len(list.Families); i++ {
				require.Less(t, list.Families[i-1].Name, list.Families[i].Name, "families must be sorted by name")
			}
		})
	}

	t.Run("no agent on the Node", func(t *testing.T) {
		code, err := metricsStatus(t.Context(), token, "GET", "api/v1/metrics/families", url.Values{"target": {"antrea-agent/no-such-node"}}, nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusNotFound, code)
	})

	t.Run("flow-aggregator", func(t *testing.T) {
		code, err := metricsStatus(t.Context(), token, "GET", "api/v1/metrics/families", url.Values{"target": {"flow-aggregator"}}, nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusNotImplemented, code)
	})
}

func TestMetricsTap(t *testing.T) {
	skipIfMetricsDisabled(t)
	token, err := GetAccessToken(t.Context(), host)
	require.NoError(t, err)

	agentTarget := firstAgentTarget(t, token)
	metricsByTarget := map[string]string{
		metricsControllerTarget: firstFamilyWithPrefix(t, getMetricFamilies(t, token, metricsControllerTarget), "antrea_controller_"),
		agentTarget:             firstFamilyWithPrefix(t, getMetricFamilies(t, token, agentTarget), "antrea_agent_"),
		metricsSelfTarget:       "antrea_ui_build_info",
	}
	var selections []apisv1.MetricsSelection
	for target, metric := range metricsByTarget {
		selections = append(selections, apisv1.MetricsSelection{Target: target, Metrics: []string{metric}})
	}

	ctx, cancel := context.WithTimeout(t.Context(), metricsStreamTimeout)
	defer cancel()
	resp, err := metricsRequest(ctx, token, "POST", "api/v1/metrics/taps", nil, apisv1.MetricsTapRequest{
		Interval:   metricsTapInterval,
		Selections: selections,
	})
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	reader := newSSEReader(resp.Body)

	event, err := reader.Next()
	require.NoError(t, err)
	require.Equal(t, "tap", event.Name)
	var tap apisv1.MetricsTapEvent
	require.NoError(t, json.Unmarshal([]byte(event.Data), &tap))
	require.NotEmpty(t, tap.ID)
	assert.Equal(t, metricsTapInterval, tap.Interval)
	assert.ElementsMatch(t, selections, tap.Selections)

	// nextScrape returns the next scrape event. Anything else fails the test: no target is
	// expected to fail, and the selection is not being updated.
	nextScrape := func() apisv1.MetricsScrapeEvent {
		event, err := reader.Next()
		require.NoError(t, err)
		require.Equal(t, "scrape", event.Name, event.Data)
		var scrape apisv1.MetricsScrapeEvent
		require.NoError(t, json.Unmarshal([]byte(event.Data), &scrape))
		return scrape
	}

	// Two consecutive scrapes of every target.
	scrapes := map[string][]apisv1.MetricsScrapeEvent{}
	done := func() bool {
		for target := range metricsByTarget {
			if len(scrapes[target]) < 2 {
				return false
			}
		}
		return true
	}
	for !done() {
		scrape := nextScrape()
		require.Contains(t, metricsByTarget, scrape.Target)
		require.Len(t, scrape.Families, 1, "target %s", scrape.Target)
		assert.Equal(t, metricsByTarget[scrape.Target], scrape.Families[0].Name)
		assert.NotEmpty(t, scrape.Families[0].Samples, "target %s", scrape.Target)
		scrapes[scrape.Target] = append(scrapes[scrape.Target], scrape)
	}
	for target, events := range scrapes {
		assert.True(t, events[1].Timestamp.After(events[0].Timestamp), "target %s was sent the same scrape twice", target)
	}
	buildInfo := scrapes[metricsSelfTarget][0].Families[0].Samples[0]
	assert.Equal(t, "1", buildInfo.Value)
	assert.Contains(t, buildInfo.Labels, "version")

	// Swap the metric of one target and drop another target, on the same stream.
	updated := []apisv1.MetricsSelection{
		{Target: metricsSelfTarget, Metrics: []string{"go_goroutines"}},
		{Target: metricsControllerTarget, Metrics: []string{metricsByTarget[metricsControllerTarget]}},
	}
	path := fmt.Sprintf("api/v1/metrics/taps/%s/selections", tap.ID)
	code, err := metricsStatus(ctx, token, "PUT", path, nil, apisv1.MetricsTapSelectionsRequest{Selections: updated})
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, code)

	// Scrapes of the previous selection can still be on their way. The "tap" event is what
	// marks the switch.
	for {
		event, err := reader.Next()
		require.NoError(t, err, "the stream must stay open across an update")
		if event.Name == "scrape" {
			continue
		}
		require.Equal(t, "tap", event.Name, event.Data)
		var updatedTap apisv1.MetricsTapEvent
		require.NoError(t, json.Unmarshal([]byte(event.Data), &updatedTap))
		assert.Equal(t, tap.ID, updatedTap.ID)
		assert.Equal(t, updated, updatedTap.Selections)
		break
	}
	seen := map[string]bool{}
	for len(seen) < len(updated) {
		scrape := nextScrape()
		require.NotEqual(t, agentTarget, scrape.Target, "a target which is no longer selected must not be scraped")
		require.Len(t, scrape.Families, 1)
		if scrape.Target == metricsSelfTarget {
			assert.Equal(t, "go_goroutines", scrape.Families[0].Name)
		}
		seen[scrape.Target] = true
	}

	// A tap is its owner's: another identity cannot update it, and is not told it exists.
	otherToken := createServiceAccountWithToken(ctx, t, antreaNamespace, randName("metrics-other-"))
	code, err = metricsStatus(ctx, otherToken, "PUT", path, nil, apisv1.MetricsTapSelectionsRequest{Selections: updated})
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, code)

	// The tap ends with its stream.
	cancel()
	resp.Body.Close()
	require.Eventually(t, func() bool {
		code, err := metricsStatus(t.Context(), token, "PUT", path, nil, apisv1.MetricsTapSelectionsRequest{Selections: updated})
		return err == nil && code == http.StatusNotFound
	}, 30*time.Second, time.Second, "the tap must be gone once its stream is closed")
}

func TestMetricsAuthorization(t *testing.T) {
	skipIfMetricsDisabled(t)
	ctx := t.Context()
	adminToken, err := GetAccessToken(ctx, host)
	require.NoError(t, err)
	agentTarget := firstAgentTarget(t, adminToken)

	families := func(token, target string) (int, error) {
		return metricsStatus(ctx, token, "GET", "api/v1/metrics/families", url.Values{"target": {target}}, nil)
	}
	// openTap reports the status of a request to open a tap on target. The stream of a tap
	// which does open is closed right away.
	openTap := func(token, target string) (int, error) {
		return metricsStatus(ctx, token, "POST", "api/v1/metrics/taps", nil, apisv1.MetricsTapRequest{
			Selections: []apisv1.MetricsSelection{{Target: target, Metrics: []string{"go_goroutines"}}},
		})
	}

	t.Run("no grant", func(t *testing.T) {
		token := createServiceAccountWithToken(ctx, t, antreaNamespace, randName("metrics-none-"))
		for _, target := range []string{metricsControllerTarget, agentTarget, metricsSelfTarget} {
			code, err := families(token, target)
			require.NoError(t, err)
			assert.Equal(t, http.StatusForbidden, code, "families of %s", target)
			code, err = openTap(token, target)
			require.NoError(t, err)
			assert.Equal(t, http.StatusForbidden, code, "tap on %s", target)
		}
		for name, targetGroup := range getMetricsTargets(t, token) {
			assert.False(t, targetGroup.Allowed, name)
			assert.Empty(t, targetGroup.Targets, name)
		}
	})

	t.Run("grant for one target group", func(t *testing.T) {
		name := randName("metrics-controller-")
		token := createServiceAccountWithToken(ctx, t, antreaNamespace, name)
		createClusterRoleBinding(ctx, t, name, []rbacv1.PolicyRule{{
			APIGroups:     []string{"ui.antrea.io"},
			Resources:     []string{"metrics"},
			ResourceNames: []string{"antrea-controller"},
			Verbs:         []string{"get"},
		}}, rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: name, Namespace: antreaNamespace})

		// The binding takes a moment to reach the authorizer.
		require.Eventually(t, func() bool {
			code, err := families(token, metricsControllerTarget)
			return err == nil && code == http.StatusOK
		}, 30*time.Second, time.Second)
		code, err := openTap(token, metricsControllerTarget)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, code)

		code, err = families(token, agentTarget)
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, code)
		code, err = openTap(token, agentTarget)
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, code)

		targetGroups := getMetricsTargets(t, token)
		assert.True(t, targetGroups["antrea-controller"].Allowed)
		assert.NotEmpty(t, targetGroups["antrea-controller"].Targets)
		assert.False(t, targetGroups["antrea-agent"].Allowed)
		// The agents are not even listed for a caller who cannot read them.
		assert.Empty(t, targetGroups["antrea-agent"].Targets)
	})

	// The grant must be cluster-wide: the same rule bound in a Namespace does not count.
	t.Run("grant in a Namespace", func(t *testing.T) {
		name := randName("metrics-namespaced-")
		token := createServiceAccountWithToken(ctx, t, antreaNamespace, name)
		_, err := k8sClient.RbacV1().ClusterRoles().Create(ctx, &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Rules: []rbacv1.PolicyRule{{
				APIGroups: []string{"ui.antrea.io"},
				Resources: []string{"metrics"},
				Verbs:     []string{"get"},
			}},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = k8sClient.RbacV1().ClusterRoles().Delete(context.Background(), name, metav1.DeleteOptions{})
		})
		ns, err := createTestNamespace(ctx)
		require.NoError(t, err)
		defer deleteNamespace(context.Background(), ns)
		createRoleBinding(ctx, t, ns, name, name, rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: name, Namespace: antreaNamespace})

		// Never becomes allowed, however long the binding has had to propagate.
		assert.Never(t, func() bool {
			code, err := families(token, metricsControllerTarget)
			return err == nil && code != http.StatusForbidden
		}, 5*time.Second, time.Second)
	})
}

// The backend's own metrics, the way a Prometheus server reads them.
func TestBackendMetrics(t *testing.T) {
	skipIfMetricsDisabled(t)
	ctx := t.Context()
	backendMetrics := func(token string) (int, string, error) {
		resp, err := metricsRequest(ctx, token, "GET", "metrics", nil, nil)
		if err != nil {
			return 0, "", err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body), err
	}

	t.Run("without the /metrics rule", func(t *testing.T) {
		token := createServiceAccountWithToken(ctx, t, antreaNamespace, randName("prometheus-none-"))
		code, body, err := backendMetrics(token)
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, code)
		assert.NotContains(t, body, "antrea_ui_build_info")
	})

	t.Run("with the /metrics rule", func(t *testing.T) {
		name := randName("prometheus-")
		token := createServiceAccountWithToken(ctx, t, antreaNamespace, name)
		createClusterRoleBinding(ctx, t, name, []rbacv1.PolicyRule{{
			NonResourceURLs: []string{"/metrics"},
			Verbs:           []string{"get"},
		}}, rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: name, Namespace: antreaNamespace})

		var body string
		require.Eventually(t, func() bool {
			code, b, err := backendMetrics(token)
			body = b
			return err == nil && code == http.StatusOK
		}, 30*time.Second, time.Second)
		assert.Contains(t, body, "antrea_ui_build_info{")
		assert.Contains(t, body, "antrea_ui_http_requests_total{")

		// Reading the backend's metrics is not a grant to tap anything through the UI.
		code, err := metricsStatus(ctx, token, "GET", "api/v1/metrics/families", url.Values{"target": {metricsSelfTarget}}, nil)
		require.NoError(t, err)
		assert.Equal(t, http.StatusForbidden, code)
	})

	t.Run("without a credential", func(t *testing.T) {
		resp, err := Request(ctx, host, "GET", "metrics", nil)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	})
}
