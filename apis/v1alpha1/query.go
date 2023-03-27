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

package v1alpha1

type QueryVariable struct {
	Name  string      `json:"name"`
	Type  string      `json:"type"`
	Value interface{} `json:"value"`
}

type Query struct {
	RefID          string          `json:"refId"`
	DataSourceName string          `json:"dataSourceName"`
	QueryName      string          `json:"queryName"`
	From           string          `json:"from"`
	To             string          `json:"to"`
	IntervalMs     int32           `json:"intervalMs"`
	TimeoutMs      int32           `json:"timeoutMs"`
	MaxValues      int32           `json:"maxValues"`
	Variables      []QueryVariable `json:"variables"`
}

type SchemaField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type DataSchema struct {
	Fields []SchemaField `json:"fields"`
}

type QueryResult struct {
	RefID  string          `json:"refId"`
	Schema DataSchema      `json:"schema"`
	Values [][]interface{} `json:"values"`
}
