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

package k8s

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
)

func TestImpersonatedClient(t *testing.T) {
	config := &rest.Config{
		Host: "https://localhost:6443",
	}

	httpClient, dynamicClient, err := ImpersonatedClient(config, "kube-system", "antrea-ui-admin")
	require.NoError(t, err)
	require.NotNil(t, httpClient)
	require.NotNil(t, dynamicClient)

	// the input config must not be mutated
	assert.Zero(t, config.Impersonate)
}
