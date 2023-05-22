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

package v1alpha1

type FrontendAuthSettings struct {
	BasicEnabled     bool   `json:"basicEnabled"`
	OIDCEnabled      bool   `json:"oidcEnabled"`
	OIDCProviderName string `json:"oidcProviderName,omitempty"`
}

// FrontendSettings are global settings exposed to the frontend, which can be
// used to render some pages appropriately. These settings are not user-specific
// and not confidential (the API for these settings is not protected by any auth
// mechanism).
type FrontendSettings struct {
	Version string               `json:"version"`
	Auth    FrontendAuthSettings `json:"auth"`
}
