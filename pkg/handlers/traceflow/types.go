package traceflow

type Request struct {
	Object map[string]interface{}
}

type RequestStatus struct {
	Done bool
	Err  error
}
