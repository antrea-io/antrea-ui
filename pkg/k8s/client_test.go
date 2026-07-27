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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport"
)

func TestImpersonatedClient(t *testing.T) {
	var gotHeader http.Header
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Clone()
	}))
	defer ts.Close()

	config := &rest.Config{Host: ts.URL}
	userName := ServiceAccountUserName("kube-system", "antrea-ui-admin")

	httpClient, dynamicClient, err := ImpersonatedClient(config, http.DefaultTransport, userName)
	require.NoError(t, err)
	require.NotNil(t, dynamicClient)

	resp, err := httpClient.Get(ts.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	// the request sent on the wire must carry the impersonated identity
	assert.Equal(t, userName, gotHeader.Get(transport.ImpersonateUserHeader))
}
