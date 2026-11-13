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
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"helm.sh/helm/v4/pkg/action"
	kubefake "helm.sh/helm/v4/pkg/kube/fake"
	releasecommon "helm.sh/helm/v4/pkg/release/common"
	releasev1 "helm.sh/helm/v4/pkg/release/v1"
	"helm.sh/helm/v4/pkg/storage"
	"helm.sh/helm/v4/pkg/storage/driver"
	"k8s.io/client-go/kubernetes/fake"
)

func TestUIReleaseOwnership(t *testing.T) {
	owned := map[string]string{partOfLabel: partOfValue}
	tests := []struct {
		name       string
		release    *releasev1.Release
		wantExists bool
		wantOwned  bool
	}{
		{name: "none"},
		{
			name:       "installed by flowbench",
			release:    &releasev1.Release{Info: &releasev1.Info{Status: releasecommon.StatusDeployed}, Labels: owned},
			wantExists: true,
			wantOwned:  true,
		},
		{
			name:       "installed by someone else",
			release:    &releasev1.Release{Info: &releasev1.Info{Status: releasecommon.StatusDeployed}},
			wantExists: true,
		},
		{
			name:    "uninstalled with its history kept",
			release: &releasev1.Release{Info: &releasev1.Info{Status: releasecommon.StatusUninstalled}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The Secrets driver, as Helm uses by default: the labels are those of the Secret
			// holding the release.
			secrets := fake.NewClientset().CoreV1().Secrets("kube-system")
			cfg := &action.Configuration{
				Releases:   storage.Init(driver.NewSecrets(secrets)),
				KubeClient: &kubefake.PrintingKubeClient{Out: io.Discard},
			}
			if tt.release != nil {
				tt.release.Name = "antrea-ui"
				tt.release.Namespace = "kube-system"
				tt.release.Version = 1
				require.NoError(t, cfg.Releases.Create(tt.release))
			}
			exists, owned, err := uiReleaseOwnership(cfg, "antrea-ui")
			require.NoError(t, err)
			assert.Equal(t, tt.wantExists, exists)
			assert.Equal(t, tt.wantOwned, owned)
		})
	}
}

func TestUIValuesHelmSets(t *testing.T) {
	o := &deployOptions{uiImageTag: "dev", nodePort: 31234, helmSets: []string{"service.nodePort=30000", "service.annotations.a=b"}}
	vals, err := uiValues(o, "0123")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"type": "NodePort", "nodePort": int64(30000), "annotations": map[string]any{"a": "b"}}, vals["service"])
	assert.Equal(t, map[string]any{"flowbench.antrea.io/scale-hash": "0123"}, vals["podAnnotations"])

	o.helmSets = []string{"service.nodePort"}
	_, err = uiValues(o, "0123")
	assert.ErrorContains(t, err, `--helm-set "service.nodePort"`)
}
