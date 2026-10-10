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
	"net"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// flowAggregatorReason is why the Flow Aggregator is listed as a target group which cannot be
// tapped.
const flowAggregatorReason = "Flow Aggregator metrics are not supported yet"

// BuiltinConfig is what the built-in target groups need to discover and to reach their targets.
type BuiltinConfig struct {
	// KubeClient and DynamicClient watch the Antrea Agents: see NewAgentTargetGroup.
	KubeClient    kubernetes.Interface
	DynamicClient dynamic.Interface
	// Controller says how to reach the Antrea Controller.
	Controller ControllerConnInfoProvider
	// ScraperTokens provides the tokens of the ServiceAccount named ScraperServiceAccountName.
	ScraperTokens TokenSource
	// Self gathers the metrics of the backend itself.
	Self prometheus.Gatherer
}

// NewBuiltinRegistry builds the registry of the target groups which antrea-ui defines itself: the
// Antrea Controller, the Antrea Agents and the backend itself, plus the Flow Aggregator, which is
// listed but cannot be tapped yet. Call Run on the registry in a goroutine: until then, the Antrea
// Agents are not known.
func NewBuiltinRegistry(config BuiltinConfig) *Registry {
	scraper := NewScraper(config.ScraperTokens, (&net.Dialer{Timeout: dialTimeout}).DialContext)
	registry := NewRegistry(
		NewControllerTargetGroup(config.Controller, scraper),
		NewAgentTargetGroup(config.DynamicClient, config.KubeClient, scraper),
		NewSelfTargetGroup(config.Self),
	)
	registry.Reserve(TargetGroupFlowAggregator, flowAggregatorReason)
	return registry
}
