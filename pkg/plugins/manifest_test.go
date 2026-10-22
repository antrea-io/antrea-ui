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

// TestValidateCompatibleAntreaUI runs the same cases the frontend's plugins.test.ts runs against
// node-semver, so the two libraries cannot drift apart on which ranges are well-formed: a range
// this accepts but the frontend cannot parse would never load, and one this rejects but the
// frontend accepts would be refused for no reason.
//
// The table lives with the frontend (client/web/antrea-ui/src/testdata), because the frontend's
// image build only sees client/web.
//
// The Go library is deliberately stricter in one place: it rejects a trailing "||", which
// node-semver reads as "or anything". That case is left out of the table.
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
