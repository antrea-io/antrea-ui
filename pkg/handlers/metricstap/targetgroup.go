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

// Package metricstap implements live metrics taps: for as long as a tap is open, the selected
// metrics of the selected targets are scraped on a fixed cadence and handed to the client. Nothing
// is retained between scrapes and nothing is persisted. See docs/metrics.md.
package metricstap

import (
	"context"
	"fmt"
	"strings"
	"sync"

	dto "github.com/prometheus/client_model/go"
	"k8s.io/apimachinery/pkg/util/validation"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

// Names of the target groups. They are also the resource names the RBAC gate is checked against,
// so they are part of the API.
const (
	TargetGroupController     = "antrea-controller"
	TargetGroupAgent          = "antrea-agent"
	TargetGroupSelf           = "antrea-ui"
	TargetGroupFlowAggregator = "flow-aggregator"
)

// Codes of the errors a scrape can fail with. They are sent to clients as is, in "scrape_error"
// events.
const (
	// ErrorCodeNotFound means the target does not exist, for example an agent on a Node which
	// is gone.
	ErrorCodeNotFound = "not_found"
	// ErrorCodeUnsupported means the target group is known but this version of Antrea UI cannot
	// scrape it.
	ErrorCodeUnsupported = "unsupported"
	// ErrorCodeUnavailable means the targets of the target group cannot be listed or resolved at
	// the moment.
	ErrorCodeUnavailable = "unavailable"
	// ErrorCodeInvalidTarget means what the cluster reports about the target cannot be used to
	// reach it safely: no CA bundle, no usable address.
	ErrorCodeInvalidTarget = "invalid_target"
	// ErrorCodeCredential means antrea-ui could not obtain the credential it scrapes with.
	// #nosec G101: not credentials
	ErrorCodeCredential = "credential_error"
	// ErrorCodeUnreachable means the connection to the target could not be established.
	ErrorCodeUnreachable = "unreachable"
	// ErrorCodeTLS means the TLS handshake failed, which includes a certificate the pinned CA
	// does not vouch for.
	ErrorCodeTLS = "tls_error"
	// ErrorCodeTimeout means the scrape did not complete in time.
	ErrorCodeTimeout = "timeout"
	// ErrorCodeBadStatus means the target answered with a status other than 200.
	ErrorCodeBadStatus = "bad_status"
	// ErrorCodeTooLarge means the response exceeded the size limit.
	ErrorCodeTooLarge = "response_too_large"
	// ErrorCodeBadResponse means the response could not be parsed as Prometheus metrics.
	ErrorCodeBadResponse = "bad_response"
	// ErrorCodeFailed is for everything else.
	ErrorCodeFailed = "scrape_failed"
)

// Error is a failure to list or scrape targets.
//
// Message names the failure class and is safe to send to a client. Err is the underlying error,
// which can hold addresses and other details of the cluster's internals: it is for the backend
// log only.
type Error struct {
	Code    string
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

func (e *Error) Unwrap() error {
	return e.Err
}

func newError(code, message string, err error) *Error {
	return &Error{Code: code, Message: message, Err: err}
}

// TargetGroup is a set of targets which are discovered, scraped and authorized the same way. This
// is the seam for target groups defined by configuration or through the API later: today the set
// is fixed at startup.
type TargetGroup interface {
	// Name is the name of the target group, and the first component of the ID of its targets.
	Name() string
	// Instanced reports whether the target group has one target per instance, with the ID
	// "<name>/<instance>", rather than a single target with the ID "<name>".
	Instanced() bool
	// Targets lists the current targets of the target group. It must not contact any of them.
	Targets(ctx context.Context) ([]apisv1.MetricsTarget, error)
	// Scrape reads the metrics of one target. instance is empty unless the target group is
	// instanced. A failure should be an *Error.
	Scrape(ctx context.Context, instance string) ([]*dto.MetricFamily, error)
}

// TargetGroupStatus is what the registry knows about a target group without contacting anything.
type TargetGroupStatus struct {
	Name      string
	Available bool
	// Reason says why the target group is not available.
	Reason string
}

// Registry is the static set of target groups, built once at startup.
type Registry struct {
	// names is in registration order, which is the order target groups are listed in.
	names        []string
	targetGroups map[string]TargetGroup
	// reserved maps the name of a target group which is known but cannot be scraped to the reason
	// why.
	reserved map[string]string
}

// NewRegistry builds a registry of target groups. It panics on a duplicate name, which is a bug in
// the caller.
func NewRegistry(targetGroups ...TargetGroup) *Registry {
	r := &Registry{
		targetGroups: make(map[string]TargetGroup),
		reserved:     make(map[string]string),
	}
	for _, targetGroup := range targetGroups {
		r.mustAddName(targetGroup.Name())
		r.targetGroups[targetGroup.Name()] = targetGroup
	}
	return r
}

// Reserve registers the name of a target group which cannot be scraped. It is listed as
// unavailable, with reason, and no tap can select it.
func (r *Registry) Reserve(name, reason string) {
	r.mustAddName(name)
	r.reserved[name] = reason
}

// runner is implemented by the target groups which have something to run, such as the watches
// they learn their targets from.
type runner interface {
	Run(stopCh <-chan struct{})
}

// Run runs the target groups which have something to run, until stopCh is closed. It blocks and
// should be called from a goroutine.
func (r *Registry) Run(stopCh <-chan struct{}) {
	var wg sync.WaitGroup
	for _, targetGroup := range r.targetGroups {
		if g, ok := targetGroup.(runner); ok {
			wg.Go(func() { g.Run(stopCh) })
		}
	}
	<-stopCh
	wg.Wait()
}

func (r *Registry) mustAddName(name string) {
	if r.known(name) {
		panic(fmt.Sprintf("target group %q is registered twice", name))
	}
	r.names = append(r.names, name)
}

func (r *Registry) known(name string) bool {
	if _, ok := r.targetGroups[name]; ok {
		return true
	}
	_, ok := r.reserved[name]
	return ok
}

// Statuses lists every target group, in registration order.
func (r *Registry) Statuses() []TargetGroupStatus {
	statuses := make([]TargetGroupStatus, 0, len(r.names))
	for _, name := range r.names {
		reason, reserved := r.reserved[name]
		statuses = append(statuses, TargetGroupStatus{Name: name, Available: !reserved, Reason: reason})
	}
	return statuses
}

// target is a parsed target ID.
type target struct {
	id          string
	targetGroup string
	instance    string
}

// parseTarget splits a target ID into the name of its target group and its instance, and checks
// that the target group is known and that the ID has the shape the target group expects. It does
// not check that the target group is available.
func (r *Registry) parseTarget(id string) (target, error) {
	name, instance, hasInstance := strings.Cut(id, "/")
	if name == "" {
		return target{}, &RequestError{Kind: RequestErrorInvalid, Message: fmt.Sprintf("invalid target %q: expected <target group> or <target group>/<instance>", id)}
	}
	if !r.known(name) {
		return target{}, &RequestError{Kind: RequestErrorUnknownTargetGroup, Message: fmt.Sprintf("unknown target group %q", name)}
	}
	t := target{id: id, targetGroup: name, instance: instance}
	targetGroup, ok := r.targetGroups[name]
	if !ok {
		// A reserved target group has no targets, so there is no shape to check.
		return t, nil
	}
	switch {
	case targetGroup.Instanced() && !hasInstance:
		return target{}, &RequestError{Kind: RequestErrorInvalid, Message: fmt.Sprintf("invalid target %q: expected %s/<instance>", id, name)}
	case !targetGroup.Instanced() && hasInstance:
		return target{}, &RequestError{Kind: RequestErrorInvalid, Message: fmt.Sprintf("invalid target %q: target group %s has a single target, %q", id, name, name)}
	case targetGroup.Instanced():
		// Instances are named after Kubernetes objects.
		if errs := validation.IsDNS1123Subdomain(instance); len(errs) > 0 {
			return target{}, &RequestError{Kind: RequestErrorInvalid, Message: fmt.Sprintf("invalid target %q: %s", id, strings.Join(errs, "; "))}
		}
	}
	return t, nil
}

// RequestErrorKind tells the API layer which status a RequestError maps to.
type RequestErrorKind int

const (
	// RequestErrorInvalid is a malformed request.
	RequestErrorInvalid RequestErrorKind = iota
	// RequestErrorUnknownTargetGroup is a target ID which names a target group that does not exist.
	RequestErrorUnknownTargetGroup
	// RequestErrorUnavailableTargetGroup is a selection which names a target group that cannot be
	// scraped.
	RequestErrorUnavailableTargetGroup
)

// RequestError is a request the manager refuses because of what it asks for. Message is meant for
// the client.
type RequestError struct {
	Kind    RequestErrorKind
	Message string
}

func (e *RequestError) Error() string {
	return e.Message
}
