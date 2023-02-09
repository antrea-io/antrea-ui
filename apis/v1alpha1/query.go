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
