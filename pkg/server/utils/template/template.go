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
	"fmt"
	"regexp"
	"slices"
)

var (
	re  = regexp.MustCompile(`\{\{\s*(\w+)\s*\}\}`)
	nre = regexp.MustCompile(`\{\{(\w+)\}\}`)
)

type Template struct {
	tpl          string
	requiredVars []string
}

func New(tpl string, supportedVars []string) (*Template, error) {
	normalizedTpl := re.ReplaceAllString(tpl, `{{$1}}`)
	matches := nre.FindAllStringSubmatch(normalizedTpl, -1)
	requiredVars := make([]string, 0, len(supportedVars))
	for _, m := range matches {
		v := m[1]
		if !slices.Contains(supportedVars, v) {
			return nil, fmt.Errorf("unknown var in template: '%s'", v)
		}
		requiredVars = append(requiredVars, v)
	}
	return &Template{
		tpl:          normalizedTpl,
		requiredVars: requiredVars,
	}, nil
}

func (t *Template) Replace(values map[string]string) (string, error) {
	for _, v := range t.requiredVars {
		if _, ok := values[v]; !ok {
			return "", fmt.Errorf("required var is missing: '%s'", v)
		}
	}
	return nre.ReplaceAllStringFunc(t.tpl, func(s string) string {
		v := s[2 : len(s)-2]
		return values[v]
	}), nil
}
