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
	"context"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

// selfTargetGroup is the backend's own metrics. They are read from the registry in-process: there
// is no HTTP request, and so no credential involved.
type selfTargetGroup struct {
	gatherer prometheus.Gatherer
}

// NewSelfTargetGroup creates the target group for the metrics of the Antrea UI backend itself.
func NewSelfTargetGroup(gatherer prometheus.Gatherer) TargetGroup {
	return &selfTargetGroup{gatherer: gatherer}
}

func (g *selfTargetGroup) Name() string {
	return TargetGroupSelf
}

func (g *selfTargetGroup) Instanced() bool {
	return false
}

func (g *selfTargetGroup) Targets(context.Context) ([]apisv1.MetricsTarget, error) {
	return []apisv1.MetricsTarget{{ID: TargetGroupSelf}}, nil
}

func (g *selfTargetGroup) Scrape(context.Context, string) ([]*dto.MetricFamily, error) {
	families, err := g.gatherer.Gather()
	if err != nil {
		return nil, newError(ErrorCodeFailed, "failed to gather the metrics of Antrea UI", err)
	}
	return families, nil
}
