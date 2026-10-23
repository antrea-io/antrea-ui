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

package plugins

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateCompatibleAntreaUI runs the cases in the table the frontend's plugins.test.ts also
// reads. The table means "the backend accepts it": this asserts every row, while the frontend only
// asserts that the valid rows parse in node-semver. The backend's grammar is narrower than
// node-semver's on purpose (see compatibleAntreaUIRegexp), so a range it accepts is always one
// every frontend can parse; the invalid rows include syntax only one of the two libraries accepts.
//
// The table lives with the frontend (client/web/antrea-ui/src/testdata), because the frontend's
// image build only sees client/web.
func TestValidateCompatibleAntreaUI(t *testing.T) {
	data, err := os.ReadFile("../../client/web/antrea-ui/src/testdata/compatible_antrea_ui.json")
	require.NoError(t, err)
	var cases []struct {
		Range string `json:"range"`
		Valid bool   `json:"valid"`
	}
	require.NoError(t, json.Unmarshal(data, &cases))
	for _, tc := range cases {
		t.Run(tc.Range, func(t *testing.T) {
			err := validateCompatibleAntreaUI(tc.Range)
			if tc.Valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestParseManifestRequiresCompatibleAntreaUI(t *testing.T) {
	_, err := parseManifest([]byte(`{"name":"plugin","version":"0.1.0","entry":"index.js"}`))
	assert.ErrorContains(t, err, "missing 'compatibleAntreaUI'")

	m, err := parseManifest([]byte(`{"name":"plugin","version":"0.1.0","entry":"index.js","compatibleAntreaUI":">=0.8.0-0 <0.9.0"}`))
	require.NoError(t, err)
	assert.Equal(t, ">=0.8.0-0 <0.9.0", m.CompatibleAntreaUI)
}
