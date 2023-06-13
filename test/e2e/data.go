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

package e2e

import (
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	apisv1alpha1 "antrea.io/antrea-ui/apis/v1alpha1"
)

const (
	antreaNamespace = "kube-system"
	antreaUIPort    = 3000
)

var (
	k8sRESTConfig *rest.Config
	k8sClient     kubernetes.Interface

	// the host address for accessing the Antrea UI
	host string

	settings *apisv1alpha1.FrontendSettings
)
