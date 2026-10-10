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

package metricstap

import (
	"context"
	"errors"
	"time"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

//go:generate mockgen -source=interface.go -package=testing -destination=testing/mock_interface.go -copyright_file=$MOCKGEN_COPYRIGHT_FILE

// Names of the events of a tap, which are also the names of the SSE events sent to the client.
const (
	// EventTap carries an apisv1.MetricsTapEvent.
	EventTap = "tap"
	// EventScrape carries an apisv1.MetricsScrapeEvent.
	EventScrape = "scrape"
	// EventScrapeError carries an apisv1.MetricsScrapeErrorEvent.
	EventScrapeError = "scrape_error"
	// EventError carries an apisv1.MetricsTapErrorEvent. It is the last event of the tap.
	EventError = "error"
)

// Codes of the terminal errors of a tap.
const (
	TapErrorCodeForbidden       = "forbidden"
	TapErrorCodeUnauthenticated = "unauthenticated"
	TapErrorCodeAuthorization   = "authorization_unavailable"
)

// Event is one event of a tap.
type Event struct {
	Name string
	// Data is the payload, one of the apisv1 event types depending on Name.
	Data any
}

var (
	// ErrTooManyTaps is returned by Open when the global cap on open taps is reached.
	ErrTooManyTaps = errors.New("too many metrics taps are open")
	// ErrTooManyTapsForUser is returned by Open when the user already has as many open taps as
	// one user can have.
	ErrTooManyTapsForUser = errors.New("too many metrics taps are open for this user")
	// ErrTapNotFound is returned when there is no tap with the given ID for the given owner.
	// A tap which belongs to someone else is not distinguished from one which does not exist.
	ErrTapNotFound = errors.New("metrics tap not found")

	// ErrForbidden is what an Authorizer returns when the caller is not allowed to read one of
	// the target groups.
	ErrForbidden = errors.New("not allowed to read the metrics of this target group")
	// ErrUnauthenticated is what an Authorizer returns when the credential of the caller was
	// rejected.
	ErrUnauthenticated = errors.New("the credential is no longer valid")
)

// Authorizer reports whether the user behind ctx may read the metrics of every one of targetGroups.
// It returns nil when they may, ErrForbidden when they may not, ErrUnauthenticated when their
// credential was rejected, and any other error when the question could not be answered.
//
// A tap calls it periodically for the target groups of its current selection, with the context it
// was opened with. It is a function so that the tap loop stays free of HTTP and Kubernetes types.
type Authorizer func(ctx context.Context, targetGroups []string) error

// TapOptions describes a tap to open.
type TapOptions struct {
	// Owner identifies who opens the tap. Only the same owner can update it.
	Owner string
	// User identifies who the tap counts against, for the cap on the open taps of one user.
	// Several owners can have the same one: the sessions of a user. Owners which do not have
	// the same one are bounded together by the global cap only.
	User string
	// Interval is the scrape interval. See Manager.ParseInterval.
	Interval time.Duration
	// Selections is the initial selection, of at least one target.
	Selections []apisv1.MetricsSelection
	// Authorizer is called periodically to check that the owner may still read what the tap
	// selects. The caller is expected to have checked it once already for Selections.
	Authorizer Authorizer
}

// Manager opens and updates metrics taps, and answers the questions a client asks before opening
// one: which targets exist, and what they expose.
type Manager interface {
	// TargetGroups lists every target group, in a stable order. It contacts nothing.
	TargetGroups() []TargetGroupStatus
	// Targets lists the current targets of the target group with the given name. It contacts no
	// target. A failure is an *Error.
	Targets(ctx context.Context, targetGroup string) ([]apisv1.MetricsTarget, error)
	// ResolveTarget returns the name of the target group of the target with the given ID. It fails
	// with a *RequestError when the ID is malformed or names an unknown target group. It succeeds
	// for a target group which is not available: the caller is expected to authorize the request
	// against the target group first.
	ResolveTarget(id string) (string, error)
	// Families scrapes the target with the given ID once and describes the metric families it
	// exposes, sorted by name. A failure is an *Error, with the code ErrorCodeUnsupported for
	// a target group which is not available and ErrorCodeNotFound for a target which does not
	// exist.
	Families(ctx context.Context, id string) (*apisv1.MetricFamilyList, error)
	// ParseInterval parses the scrape interval a client asked for, an empty string meaning the
	// default. It fails with a *RequestError when the interval is malformed or out of range.
	ParseInterval(interval string) (time.Duration, error)
	// ValidateSelections checks the selections of a tap and returns the distinct names of the
	// target groups they refer to, sorted. It fails with a *RequestError.
	ValidateSelections(selections []apisv1.MetricsSelection) ([]string, error)
	// Open opens a tap, which lives until ctx ends or until it sends an EventError. It returns
	// the ID of the tap and the channel of its events, which starts with an EventTap and is
	// closed when the tap ends. The caller must receive from the channel until it is closed or
	// ctx ends. It fails with a *RequestError when the selections are not valid, and with
	// ErrTooManyTaps or ErrTooManyTapsForUser.
	Open(ctx context.Context, options TapOptions) (string, <-chan Event, error)
	// HasTap reports whether owner has an open tap with the given ID.
	HasTap(id, owner string) bool
	// UpdateSelections replaces the whole selection of an open tap. The new selection applies
	// from the next tick of the tap, which keeps its ID and its cadence. It fails with a
	// *RequestError when the selections are not valid, and with ErrTapNotFound.
	UpdateSelections(id, owner string, selections []apisv1.MetricsSelection) error
}
