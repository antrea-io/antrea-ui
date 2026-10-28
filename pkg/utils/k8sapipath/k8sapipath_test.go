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

package k8sapipath

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateGroupPath(t *testing.T) {
	tests := []struct {
		path    string
		wantErr bool
	}{
		{path: "/apis/ui.example.com/v1"},
		{path: "/apis/ui.example.com/v1beta2"},
		{path: "/apis/ui.example.com/v2alpha1"},
		{path: "/apis/apps/v1"},
		{path: "/apis/ui.example.com/v1/", wantErr: true},
		{path: "/apis/ui.example.com/v1/uipluginbundles", wantErr: true},
		{path: "/apis/ui.example.com", wantErr: true},
		{path: "/apis/ui.example.com/1", wantErr: true},
		{path: "/apis/ui.example.com/v0", wantErr: true},
		{path: "/apis/UI.example.com/v1", wantErr: true},
		{path: "/api/v1", wantErr: true},
		{path: "/api/v1/namespaces/ns/services/svc/proxy", wantErr: true},
		{path: "/apis/ui.example.com/v1/proxy", wantErr: true},
		{path: "/x", wantErr: true},
		{path: "", wantErr: true},
		{path: "apis/x", wantErr: true},
		{path: "//evil.example/apis/x", wantErr: true},
		{path: "https://evil.example/apis/x", wantErr: true},
		{path: "/apis/x?watch=true", wantErr: true},
		{path: "/apis/x#frag", wantErr: true},
		// A valid group with a query or fragment after the version: only the version check
		// rejects these.
		{path: "/apis/ui.example.com/v1?watch=true", wantErr: true},
		{path: "/apis/ui.example.com/v1#frag", wantErr: true},
		{path: "/apis/../api/v1/secrets", wantErr: true},
		{path: "/apis/./x", wantErr: true},
		{path: "/apis/../v1", wantErr: true},
		// Four segments, each wrong in only one place: these are rejected by the leading "/", the
		// "apis" segment and the start of the version respectively.
		{path: "x/apis/ui.example.com/v1", wantErr: true},
		{path: "/api/ui.example.com/v1", wantErr: true},
		{path: "/apis/ui.example.com/xv1", wantErr: true},
		{path: "/apis/ui.example.com/..", wantErr: true},
		{path: "/apis//x", wantErr: true},
		{path: "/apis/%2e%2e/x", wantErr: true},
		{path: `/apis\x`, wantErr: true},
		{path: "/", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			err := ValidateGroupPath(tt.path)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
