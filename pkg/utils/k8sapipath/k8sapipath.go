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

// Package k8sapipath validates the base paths of Kubernetes API groups that the backend is told to
// send requests under, by a plugin manifest or by its configuration.
package k8sapipath

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

// apiVersionRegexp matches a Kubernetes API version: v1, v1beta2, v2alpha1...
var apiVersionRegexp = regexp.MustCompile(`^v[1-9][0-9]*((alpha|beta)[1-9][0-9]*)?$`)

// ValidateGroupPath checks that p is exactly /apis/<group>/<version>, the base path of an API
// group, meant to be one served by an aggregated API server. The backend sends requests under it
// with privileged credentials (the antrea-ui ServiceAccount for a plugin bundle download,
// antrea-ui-admin for a support bundle source), and only ever appends paths of its own to it:
// "/uipluginbundles/<sha256>/download" for a bundle download, and
// "/supportbundles[/<id>[/download]]" for a support bundle source. So a path that passes
// can only make it request those resources of that group: no core "/api/..." path, no "../"
// traversal, no ".../proxy" subresource, no query.
func ValidateGroupPath(p string) error {
	if p == "" {
		return errors.New("path is missing")
	}
	parts := strings.Split(p, "/")
	if len(parts) != 4 || parts[0] != "" || parts[1] != "apis" {
		return fmt.Errorf("path %q must be of the form /apis/<group>/<version>", p)
	}
	if errs := validation.IsDNS1123Subdomain(parts[2]); len(errs) > 0 {
		return fmt.Errorf("path %q has an invalid group %q: %s", p, parts[2], strings.Join(errs, "; "))
	}
	if !apiVersionRegexp.MatchString(parts[3]) {
		return fmt.Errorf("path %q has an invalid version %q", p, parts[3])
	}
	return nil
}
