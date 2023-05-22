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

package template

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	testCases := []struct {
		name                 string
		tpl                  string
		supportedVars        []string
		expectedErr          string
		expectedTpl          string
		expectedRequiredVars []string
	}{
		{
			name: "empty",
		},
		{
			name:        "no vars",
			tpl:         "foo/bar",
			expectedTpl: "foo/bar",
		},
		{
			name:                 "single var",
			tpl:                  "hello {{name}}",
			supportedVars:        []string{"name"},
			expectedTpl:          "hello {{name}}",
			expectedRequiredVars: []string{"name"},
		},
		{
			name:                 "single var with space",
			tpl:                  "hello {{ name }}",
			supportedVars:        []string{"name"},
			expectedTpl:          "hello {{name}}",
			expectedRequiredVars: []string{"name"},
		},
		{
			name:        "unsupported var",
			tpl:         "hello {{ name }}",
			expectedErr: "unknown var in template",
		},
		{
			name:                 "several vars",
			tpl:                  "hello {{ you }}, my name is {{me}}, I am from {{  _Location}}",
			supportedVars:        []string{"me", "you", "_Location"},
			expectedTpl:          "hello {{you}}, my name is {{me}}, I am from {{_Location}}",
			expectedRequiredVars: []string{"you", "me", "_Location"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			template, err := New(tc.tpl, tc.supportedVars)
			if tc.expectedErr != "" {
				assert.ErrorContains(t, err, tc.expectedErr)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.expectedTpl, template.tpl)
				assert.ElementsMatch(t, tc.expectedRequiredVars, template.requiredVars)
			}
		})
	}
}

func TestReplace(t *testing.T) {
	template, err := New("hello {{ you }}, my name is {{me}}, I am from {{  _Location}}", []string{"me", "you", "_Location"})
	require.NoError(t, err)

	t.Run("success", func(t *testing.T) {
		s, err := template.Replace(map[string]string{
			"you":       "Bob",
			"me":        "Alice",
			"_Location": "Palo Alto",
			// extra var should be ignored
			"foo": "bar",
		})
		require.NoError(t, err)
		assert.Equal(t, "hello Bob, my name is Alice, I am from Palo Alto", s)
	})

	t.Run("missing var", func(t *testing.T) {
		_, err := template.Replace(map[string]string{
			"you": "Bob",
			"me":  "Alice",
		})
		assert.ErrorContains(t, err, "required var is missing")
	})
}
