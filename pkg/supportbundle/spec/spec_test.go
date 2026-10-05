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

package spec

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateAPIServerPath(t *testing.T) {
	for _, path := range []string{
		"/apis/foo.example.com/v1",
		"/apis/foo.example.com/v1alpha1",
		"/apis/stats.antrea.io/v1beta2",
		"/apis/apps/v1",
	} {
		assert.NoError(t, ValidateAPIServerPath(path), path)
	}
	for _, path := range []string{
		"",
		"/",
		"/api/v1",
		"/apis/foo.example.com",
		"/apis/foo.example.com/v1/",
		"/apis/foo.example.com/v1/namespaces",
		"/apis/foo.example.com/v1/../../api",
		"/apis/../v1",
		"/apis/foo.example.com/..",
		"/apis/Foo.example.com/v1",
		"/apis/foo.example.com/v1?x=y",
		"/apis/foo.example.com/v1#x",
		"/apis/foo.example.com/version1",
		"/apis/foo.example.com/v0",
		"apis/foo.example.com/v1",
		"//apis/foo.example.com/v1",
		"https://example.com/apis/foo.example.com/v1",
	} {
		assert.Error(t, ValidateAPIServerPath(path), path)
	}
}
