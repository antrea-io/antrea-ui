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

package metricstap

import (
	"testing"
	"testing/synctest"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

func TestBuiltinRegistry(t *testing.T) {
	// A bubble: the watches of the agent target group are backed by fake clients and do no real
	// I/O, so the bubble can tell when they have nothing left to do.
	synctest.Test(t, func(t *testing.T) {
		registry := NewBuiltinRegistry(BuiltinConfig{
			KubeClient: k8sfake.NewSimpleClientset(nodeWithAddresses("node-a", internalIP("192.0.2.1"))),
			DynamicClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
				map[schema.GroupVersionResource]string{antreaAgentInfoGVR: "AntreaAgentInfoList"},
				agentInfo("node-a", testAgentPort, nil)),
			Controller:    &fakeControllerConnInfo{},
			ScraperTokens: &staticTokenSource{token: testToken},
			Self:          prometheus.NewRegistry(),
		})
		assert.Equal(t, []TargetGroupStatus{
			{Name: TargetGroupController, Available: true},
			{Name: TargetGroupAgent, Available: true},
			{Name: TargetGroupSelf, Available: true},
			{Name: TargetGroupFlowAggregator, Available: false, Reason: "Flow Aggregator metrics are not supported yet"},
		}, registry.Statuses())

		// The agents are only known once the registry runs.
		agents := registry.targetGroups[TargetGroupAgent]
		_, err := agents.Targets(t.Context())
		requireScrapeError(t, err, ErrorCodeUnavailable)

		stopCh := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			registry.Run(stopCh)
		}()
		synctest.Wait()
		targets, err := agents.Targets(t.Context())
		require.NoError(t, err)
		assert.Equal(t, []apisv1.MetricsTarget{{ID: "antrea-agent/node-a", Labels: map[string]string{"node": "node-a"}}}, targets)
		select {
		case <-done:
			require.Fail(t, "Run should block until stopCh is closed")
		default:
		}

		close(stopCh)
		<-done
	})
}
