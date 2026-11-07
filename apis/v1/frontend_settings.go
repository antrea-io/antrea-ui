// Copyright 2023 Antrea Authors.
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

type FrontendAuthSettings struct {
	// BasicEnabled is the static admin password (mode 4).
	BasicEnabled bool `json:"basicEnabled"`
	// OIDCEnabled is login through an OIDC provider the kube-apiserver also trusts (mode 1).
	OIDCEnabled      bool   `json:"oidcEnabled"`
	OIDCProviderName string `json:"oidcProviderName,omitempty"`
	// KubeconfigEnabled is "bring your own kubeconfig" (mode 3).
	KubeconfigEnabled bool `json:"kubeconfigEnabled"`
	// TokenEnabled is "paste a Kubernetes token" (mode 5).
	TokenEnabled bool `json:"tokenEnabled"`
}

// FrontendFlowSource describes a configured flow source. It carries what a UI needs to decide what
// to offer, and nothing about where the source is or how it is reached: the settings are served
// without authentication.
type FrontendFlowSource struct {
	// Name is what a request passes as source=<name>.
	Name string `json:"name"`
	// Capabilities lists what the source serves: "watch" (live streams) and/or "list" (finite
	// queries).
	Capabilities []string `json:"capabilities"`
}

type FrontendFeatureSettings struct {
	// FlowVisibilityEnabled reports whether at least one flow source is configured.
	FlowVisibilityEnabled bool `json:"flowVisibilityEnabled"`
	// FlowSources lists the configured flow sources, in the order in which they are the default.
	// It is empty, never null, when there are none.
	FlowSources []FrontendFlowSource `json:"flowSources"`
	// MetricsEnabled reports whether the live metrics tap API (/api/v1/metrics) is available.
	MetricsEnabled bool `json:"metricsEnabled"`
}

// FrontendSettings are global settings exposed to the frontend, which can be
// used to render some pages appropriately. These settings are not user-specific
// and not confidential (the API for these settings is not protected by any auth
// mechanism).
type FrontendSettings struct {
	Version  string                  `json:"version"`
	Auth     FrontendAuthSettings    `json:"auth"`
	Features FrontendFeatureSettings `json:"features"`
}
