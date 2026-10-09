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

	dto "github.com/prometheus/client_model/go"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

// ControllerConnInfoProvider gives the current way to reach the Antrea Controller API. The handler
// for Antrea Service requests implements it: it already tracks the rotation of the Antrea CA, and
// the port forwarding used when the backend runs outside of the cluster.
type ControllerConnInfoProvider interface {
	// ConnInfo returns the host to connect to, the name the server certificate is issued for
	// and the current CA bundle.
	ConnInfo() (host string, serverName string, caBundle []byte, err error)
}

// controllerTargetGroup is the Antrea Controller, reached through the antrea Service.
type controllerTargetGroup struct {
	provider ControllerConnInfoProvider
	scraper  *Scraper
}

// NewControllerTargetGroup creates the target group for the metrics of the Antrea Controller.
func NewControllerTargetGroup(provider ControllerConnInfoProvider, scraper *Scraper) TargetGroup {
	return &controllerTargetGroup{provider: provider, scraper: scraper}
}

func (g *controllerTargetGroup) Name() string {
	return TargetGroupController
}

func (g *controllerTargetGroup) Instanced() bool {
	return false
}

func (g *controllerTargetGroup) Targets(context.Context) ([]apisv1.MetricsTarget, error) {
	return []apisv1.MetricsTarget{{ID: TargetGroupController}}, nil
}

func (g *controllerTargetGroup) Scrape(ctx context.Context, _ string) ([]*dto.MetricFamily, error) {
	host, serverName, caBundle, err := g.provider.ConnInfo()
	if err != nil {
		return nil, newError(ErrorCodeUnavailable, "the connection to the Antrea Controller is not ready", err)
	}
	return g.scraper.Scrape(ctx, ConnInfo{Host: host, ServerName: serverName, CABundle: caBundle})
}
