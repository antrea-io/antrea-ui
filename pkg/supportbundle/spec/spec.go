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

// Package spec holds the validation shared by every place a support bundle source can be declared:
// a plugin manifest and the backend configuration.
package spec

import (
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

var apiVersionRegexp = regexp.MustCompile(`^v[1-9][0-9]*((alpha|beta)[1-9][0-9]*)?$`)

// ValidateAPIServerPath checks that path is exactly /apis/<group>/<version>.
//
// The path is requested as antrea-ui-admin (antrea-ui impersonating it), so the shape is what
// keeps a source declaration from pointing antrea-ui at an arbitrary endpoint. With
// "/supportbundle" appended, an aggregated API group path can only ever reach a resource named
// "supportbundle" of that group: no core "/api/..." path, no "../" traversal, no ".../proxy"
// subresource, no query.
func ValidateAPIServerPath(path string) error {
	parts := strings.Split(path, "/")
	if len(parts) != 4 || parts[0] != "" || parts[1] != "apis" {
		return fmt.Errorf("path %q must be of the form /apis/<group>/<version>", path)
	}
	if errs := validation.IsDNS1123Subdomain(parts[2]); len(errs) > 0 {
		return fmt.Errorf("path %q has an invalid group %q: %s", path, parts[2], strings.Join(errs, "; "))
	}
	if !apiVersionRegexp.MatchString(parts[3]) {
		return fmt.Errorf("path %q has an invalid version %q", path, parts[3])
	}
	return nil
}
