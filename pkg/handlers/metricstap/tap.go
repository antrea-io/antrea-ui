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
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	apisv1 "antrea.io/antrea-ui/apis/v1"
	"antrea.io/antrea-ui/pkg/utils/random"
)

const (
	// DefaultScrapeInterval is the interval of a tap which does not ask for one.
	DefaultScrapeInterval = 10 * time.Second

	// maxMetricsPerSelection bounds the metric families a tap reads from one target.
	maxMetricsPerSelection = 100
	// maxMetricNameLength bounds the length of a metric name in a selection.
	maxMetricNameLength = 256
	// maxSamplesPerEvent bounds the samples of one "scrape" event. A family with many series
	// (or many buckets) is cut, and the event says so.
	maxSamplesPerEvent = 5000
	// maxTapsPerUser bounds the taps one user can have open, whatever their owners, so that one
	// user cannot use up the global cap. The caller decides what a user is: see TapOptions.User.
	maxTapsPerUser = 5
	// tapEventBuffer is the capacity of the event channel of a tap. A client which reads
	// slower than the tap produces makes the tap wait, it does not make it drop events.
	tapEventBuffer = 16

	// reauthorizeInterval is how often an open tap checks that its owner may still read what it
	// selects. A tap whose grant was removed ends at the first check which follows, and a check
	// which is due waits for the tick in progress.
	reauthorizeInterval = time.Minute
	// maxAuthorizationFailures is how many consecutive checks can fail to get an answer before
	// the tap is closed. Until then, the tap keeps the last answer it got.
	maxAuthorizationFailures = 3
	// authorizationTimeout bounds one check.
	authorizationTimeout = 10 * time.Second
)

// Observer is told about the taps and scrapes of a manager. pkg/metrics implements it.
type Observer interface {
	ScrapeObserver
	TapOpened()
	TapClosed()
}

// ManagerOptions configures a manager.
type ManagerOptions struct {
	// MinScrapeInterval and MaxScrapeInterval bound the interval a tap can ask for.
	MinScrapeInterval time.Duration
	MaxScrapeInterval time.Duration
	// MaxTaps bounds the taps open at once, across all owners.
	MaxTaps int
	// MaxTargetsPerTap bounds the targets of one tap.
	MaxTargetsPerTap int
	// Observer is optional.
	Observer Observer
}

// tapSelection is a validated selection.
type tapSelection struct {
	target  target
	metrics []string
}

// tapSelections is the whole selection of a tap, in the form it is reported in (api) and in the
// form it is scraped from.
type tapSelections struct {
	api          []apisv1.MetricsSelection
	parsed       []tapSelection
	targetGroups []string
}

type tap struct {
	id       string
	owner    string
	user     string
	interval time.Duration
	events   chan Event

	// mutex guards pending and lastEmitted.
	mutex sync.Mutex
	// pending is a selection update which the loop has not applied yet.
	pending *tapSelections
	// updated wakes the loop up when pending is set.
	updated chan struct{}
	// lastEmitted maps the ID of a target to the timestamp of the last result sent for it.
	lastEmitted map[string]time.Time

	// active is the selection in effect. It belongs to the loop.
	active *tapSelections
}

type manager struct {
	logger    logr.Logger
	registry  *Registry
	coalescer *coalescer
	options   ManagerOptions

	// mutex guards taps and tapsPerUser.
	mutex       sync.Mutex
	taps        map[string]*tap
	tapsPerUser map[string]int
}

// NewManager creates a Manager for the target groups of registry. It runs nothing in the
// background: a tap lives in the goroutine started by Open, which ends with the context of the tap.
func NewManager(logger logr.Logger, registry *Registry, options ManagerOptions) Manager {
	var scrapeObserver ScrapeObserver
	if options.Observer != nil {
		scrapeObserver = options.Observer
	}
	return &manager{
		logger:      logger,
		registry:    registry,
		coalescer:   newCoalescer(logger, options.MinScrapeInterval, scrapeObserver),
		options:     options,
		taps:        make(map[string]*tap),
		tapsPerUser: make(map[string]int),
	}
}

func (m *manager) TargetGroups() []TargetGroupStatus {
	return m.registry.Statuses()
}

func (m *manager) Targets(ctx context.Context, targetGroup string) ([]apisv1.MetricsTarget, error) {
	g, ok := m.registry.targetGroups[targetGroup]
	if !ok {
		return nil, newError(ErrorCodeUnavailable, fmt.Sprintf("target group %q has no targets", targetGroup), nil)
	}
	targets, err := g.Targets(ctx)
	if err != nil {
		return nil, err
	}
	if targets == nil {
		targets = []apisv1.MetricsTarget{}
	}
	return targets, nil
}

func (m *manager) ResolveTarget(id string) (string, error) {
	t, err := m.registry.parseTarget(id)
	if err != nil {
		return "", err
	}
	return t.targetGroup, nil
}

func (m *manager) Families(ctx context.Context, id string) (*apisv1.MetricFamilyList, error) {
	t, err := m.registry.parseTarget(id)
	if err != nil {
		return nil, err
	}
	targetGroup, ok := m.registry.targetGroups[t.targetGroup]
	if !ok {
		return nil, newError(ErrorCodeUnsupported, m.registry.reserved[t.targetGroup], nil)
	}
	ctx, cancel := context.WithTimeout(ctx, maxScrapeTimeout)
	defer cancel()
	result, err := m.coalescer.scrape(ctx, targetGroup, t)
	if err != nil {
		return nil, newError(ErrorCodeTimeout, "the scrape timed out", err)
	}
	if result.err != nil {
		return nil, result.err
	}
	return &apisv1.MetricFamilyList{
		Target:    t.id,
		Timestamp: eventTime(result.timestamp),
		Families:  describeFamilies(result.families),
	}, nil
}

func (m *manager) ParseInterval(interval string) (time.Duration, error) {
	if interval == "" {
		// The default must be usable whatever the configured bounds are.
		return min(max(DefaultScrapeInterval, m.options.MinScrapeInterval), m.options.MaxScrapeInterval), nil
	}
	d, err := time.ParseDuration(interval)
	if err != nil {
		return 0, &RequestError{Kind: RequestErrorInvalid, Message: fmt.Sprintf("invalid interval %q: expected a duration such as 10s", interval)}
	}
	if d < m.options.MinScrapeInterval || d > m.options.MaxScrapeInterval {
		return 0, &RequestError{Kind: RequestErrorInvalid, Message: fmt.Sprintf("invalid interval %s: must be between %s and %s", d, m.options.MinScrapeInterval, m.options.MaxScrapeInterval)}
	}
	return d, nil
}

func (m *manager) ValidateSelections(selections []apisv1.MetricsSelection) ([]string, error) {
	parsed, err := m.parseSelections(selections)
	if err != nil {
		return nil, err
	}
	return parsed.targetGroups, nil
}

func (m *manager) parseSelections(selections []apisv1.MetricsSelection) (*tapSelections, error) {
	invalid := func(format string, args ...any) error {
		return &RequestError{Kind: RequestErrorInvalid, Message: fmt.Sprintf(format, args...)}
	}
	// A tap always selects something: every open tap is then one whose owner passed an access
	// review, and which the periodic check covers.
	if len(selections) == 0 {
		return nil, invalid("no target is selected: a tap selects at least one")
	}
	if len(selections) > m.options.MaxTargetsPerTap {
		return nil, invalid("too many targets: a tap can select at most %d, but %d were given", m.options.MaxTargetsPerTap, len(selections))
	}
	result := &tapSelections{
		api:    make([]apisv1.MetricsSelection, 0, len(selections)),
		parsed: make([]tapSelection, 0, len(selections)),
	}
	targets := make(map[string]bool, len(selections))
	targetGroups := make(map[string]bool)
	for _, selection := range selections {
		t, err := m.registry.parseTarget(selection.Target)
		if err != nil {
			return nil, err
		}
		if reason, reserved := m.registry.reserved[t.targetGroup]; reserved {
			return nil, &RequestError{Kind: RequestErrorUnavailableTargetGroup, Message: fmt.Sprintf("invalid target %q: %s", t.id, reason)}
		}
		if targets[t.id] {
			return nil, invalid("target %q is selected more than once", t.id)
		}
		targets[t.id] = true
		if len(selection.Metrics) == 0 {
			return nil, invalid("the selection for target %q names no metric", t.id)
		}
		if len(selection.Metrics) > maxMetricsPerSelection {
			return nil, invalid("too many metrics for target %q: at most %d can be selected, but %d were given", t.id, maxMetricsPerSelection, len(selection.Metrics))
		}
		names := make(map[string]bool, len(selection.Metrics))
		for _, name := range selection.Metrics {
			if name == "" || len(name) > maxMetricNameLength {
				return nil, invalid("invalid metric name for target %q: must be between 1 and %d characters", t.id, maxMetricNameLength)
			}
			if names[name] {
				return nil, invalid("metric %q is selected more than once for target %q", name, t.id)
			}
			names[name] = true
		}
		// The slices outlive the request they were decoded from, and are read by the loop of
		// the tap.
		metrics := slices.Clone(selection.Metrics)
		result.api = append(result.api, apisv1.MetricsSelection{Target: t.id, Metrics: metrics})
		result.parsed = append(result.parsed, tapSelection{target: t, metrics: metrics})
		targetGroups[t.targetGroup] = true
	}
	for name := range targetGroups {
		result.targetGroups = append(result.targetGroups, name)
	}
	sort.Strings(result.targetGroups)
	return result, nil
}

func (m *manager) Open(ctx context.Context, options TapOptions) (string, <-chan Event, error) {
	selections, err := m.parseSelections(options.Selections)
	if err != nil {
		return "", nil, err
	}
	// 128 random bits: the ID is not guessable. Ownership is checked on top of that.
	id, err := random.HexString(16)
	if err != nil {
		return "", nil, err
	}
	t := &tap{
		id:          id,
		owner:       options.Owner,
		user:        options.User,
		interval:    options.Interval,
		events:      make(chan Event, tapEventBuffer),
		updated:     make(chan struct{}, 1),
		lastEmitted: make(map[string]time.Time),
		active:      selections,
	}

	m.mutex.Lock()
	if len(m.taps) >= m.options.MaxTaps {
		m.mutex.Unlock()
		return "", nil, ErrTooManyTaps
	}
	if m.tapsPerUser[t.user] >= maxTapsPerUser {
		m.mutex.Unlock()
		return "", nil, ErrTooManyTapsForUser
	}
	m.taps[t.id] = t
	m.tapsPerUser[t.user]++
	m.mutex.Unlock()
	if m.options.Observer != nil {
		m.options.Observer.TapOpened()
	}

	go m.run(ctx, t, options.Authorizer)
	return t.id, t.events, nil
}

func (m *manager) remove(t *tap) {
	m.mutex.Lock()
	delete(m.taps, t.id)
	if m.tapsPerUser[t.user] <= 1 {
		delete(m.tapsPerUser, t.user)
	} else {
		m.tapsPerUser[t.user]--
	}
	m.mutex.Unlock()
	if m.options.Observer != nil {
		m.options.Observer.TapClosed()
	}
}

// find returns the open tap with the given ID if it belongs to owner.
func (m *manager) find(id, owner string) (*tap, bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	t, ok := m.taps[id]
	if !ok || t.owner != owner {
		return nil, false
	}
	return t, true
}

func (m *manager) HasTap(id, owner string) bool {
	_, ok := m.find(id, owner)
	return ok
}

func (m *manager) UpdateSelections(id, owner string, selections []apisv1.MetricsSelection) error {
	parsed, err := m.parseSelections(selections)
	if err != nil {
		return err
	}
	t, ok := m.find(id, owner)
	if !ok {
		return ErrTapNotFound
	}
	t.mutex.Lock()
	t.pending = parsed
	t.mutex.Unlock()
	select {
	case t.updated <- struct{}{}:
	default:
		// The loop has a wake-up pending already, and will find this update when it handles
		// it: only the last update matters.
	}
	return nil
}

// run is the loop of a tap. It ends with ctx, or after it has sent a terminal error.
func (m *manager) run(ctx context.Context, t *tap, authorize Authorizer) {
	// Nothing sends on the channel once the loop has returned: the scrapes of a tick are all
	// waited for before the tick returns.
	defer close(t.events)
	defer m.remove(t)

	if !t.emitTap(ctx) {
		return
	}
	// The cadence starts with the tap and not after its first scrape, which lasts for as long
	// as the wait for a target which does not answer.
	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()
	authTicker := time.NewTicker(reauthorizeInterval)
	defer authTicker.Stop()
	// The first scrape is immediate: a client should not have to wait for a whole interval to
	// see anything.
	m.scrapeAll(ctx, t)
	authorizationFailures := 0
	// checkAccess makes the periodic access check. It reports false when it ended the tap.
	checkAccess := func() bool {
		terminal := m.reauthorize(ctx, t, authorize, &authorizationFailures)
		if terminal == nil {
			return true
		}
		t.emit(ctx, Event{Name: EventError, Data: *terminal})
		return false
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.updated:
			if !t.applyUpdate(ctx) {
				return
			}
		case <-authTicker.C:
			if !checkAccess() {
				return
			}
		case <-ticker.C:
			// An update which is pending and an access check which is due come before the
			// scrapes of the tick. Both are ready at the same time as the tick whenever they
			// came up while the loop was busy for a whole interval: with a target which does
			// not answer, or a client which does not read. This tick then uses the new
			// selection already, and scrapes nothing for an owner whose grant was removed.
			// Either way a selection is never used before the "tap" event which announces it
			// has been sent.
			select {
			case <-t.updated:
				if !t.applyUpdate(ctx) {
					return
				}
			default:
			}
			select {
			case <-authTicker.C:
				if !checkAccess() {
					return
				}
			default:
			}
			m.scrapeAll(ctx, t)
		}
	}
}

// emit sends an event to the client. It reports false when the tap ended first.
func (t *tap) emit(ctx context.Context, event Event) bool {
	select {
	case t.events <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func (t *tap) emitTap(ctx context.Context) bool {
	return t.emit(ctx, Event{Name: EventTap, Data: apisv1.MetricsTapEvent{
		ID:         t.id,
		Interval:   t.interval.String(),
		Selections: t.active.api,
	}})
}

// applyUpdate makes the pending selection the active one, and announces it.
func (t *tap) applyUpdate(ctx context.Context) bool {
	t.mutex.Lock()
	pending := t.pending
	t.pending = nil
	if pending != nil {
		// What was sent for a target is only remembered for as long as its selection stays the
		// same: a result which was sent for other metrics is new for the ones selected now.
		previous := make(map[string][]string, len(t.active.parsed))
		for _, selection := range t.active.parsed {
			previous[selection.target.id] = selection.metrics
		}
		unchanged := make(map[string]bool, len(pending.parsed))
		for _, selection := range pending.parsed {
			if metrics, ok := previous[selection.target.id]; ok && slices.Equal(metrics, selection.metrics) {
				unchanged[selection.target.id] = true
			}
		}
		for id := range t.lastEmitted {
			if !unchanged[id] {
				delete(t.lastEmitted, id)
			}
		}
	}
	t.mutex.Unlock()
	if pending == nil {
		return true
	}
	t.active = pending
	return t.emitTap(ctx)
}

// isNew records that the result with the given timestamp is being sent for the target, and
// reports false if it was the last one sent for it already. Results are shared between taps and
// reused for a while, so a tap can be handed one twice.
func (t *tap) isNew(targetID string, timestamp time.Time) bool {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	if last, ok := t.lastEmitted[targetID]; ok && last.Equal(timestamp) {
		return false
	}
	t.lastEmitted[targetID] = timestamp
	return true
}

// scrapeAll scrapes every target of the active selection and sends one event per target, as each
// scrape completes.
//
// The targets are all asked at once, and nothing bounds the scrapes in flight but the scrapes
// themselves: a target is scraped once at a time whoever asks (see coalescer), and only a target
// which exists is contacted. A target which does not answer therefore holds up no other one, in
// this tap or in another, and a tick lasts no longer than the wait for one target.
func (m *manager) scrapeAll(ctx context.Context, t *tap) {
	var wg sync.WaitGroup
	for _, selection := range t.active.parsed {
		wg.Go(func() { m.scrapeOne(ctx, t, selection) })
	}
	wg.Wait()
}

func (m *manager) scrapeOne(ctx context.Context, t *tap, selection tapSelection) {
	targetGroup := m.registry.targetGroups[selection.target.targetGroup]
	waitCtx, cancel := context.WithTimeout(ctx, min(t.interval, maxScrapeTimeout))
	defer cancel()
	result, err := m.coalescer.scrape(waitCtx, targetGroup, selection.target)
	if err != nil {
		if ctx.Err() != nil {
			// The tap ended while waiting.
			return
		}
		// This tap stopped waiting. The scrape itself may still complete for the others.
		t.emit(ctx, Event{Name: EventScrapeError, Data: apisv1.MetricsScrapeErrorEvent{
			Target:    selection.target.id,
			Timestamp: eventTime(time.Now()),
			Code:      ErrorCodeTimeout,
			Message:   "the scrape timed out",
		}})
		return
	}
	if !t.isNew(selection.target.id, result.timestamp) {
		return
	}
	if result.err != nil {
		t.emit(ctx, Event{Name: EventScrapeError, Data: apisv1.MetricsScrapeErrorEvent{
			Target:    selection.target.id,
			Timestamp: eventTime(result.timestamp),
			Code:      ErrorCode(result.err),
			Message:   ErrorMessage(result.err),
		}})
		return
	}
	families, truncated := flattenFamilies(result, selection.metrics, maxSamplesPerEvent)
	t.emit(ctx, Event{Name: EventScrape, Data: apisv1.MetricsScrapeEvent{
		Target:    selection.target.id,
		Timestamp: eventTime(result.timestamp),
		Families:  families,
		Truncated: truncated,
	}})
}

// reauthorize checks that the owner of the tap may still read the target groups of its selection.
// It returns the terminal error to end the tap with, or nil when the tap goes on.
func (m *manager) reauthorize(ctx context.Context, t *tap, authorize Authorizer, failures *int) *apisv1.MetricsTapErrorEvent {
	if authorize == nil {
		return nil
	}
	authCtx, cancel := context.WithTimeout(ctx, authorizationTimeout)
	defer cancel()
	err := authorize(authCtx, t.active.targetGroups)
	switch {
	case err == nil:
		*failures = 0
		return nil
	case errors.Is(err, ErrForbidden):
		return &apisv1.MetricsTapErrorEvent{
			Code:    TapErrorCodeForbidden,
			Message: "access to the selected metrics was revoked",
		}
	case errors.Is(err, ErrUnauthenticated):
		return &apisv1.MetricsTapErrorEvent{
			Code:    TapErrorCodeUnauthenticated,
			Message: "the session is no longer valid",
		}
	case ctx.Err() != nil:
		return nil
	}
	// No answer is not a denial: the tap keeps the last answer it got, but not for ever, or a
	// revoked grant would go unnoticed for as long as the checks keep failing.
	*failures++
	m.logger.Error(err, "Failed to check the authorization of a metrics tap", "tap", t.id, "consecutiveFailures", *failures)
	if *failures > maxAuthorizationFailures {
		return &apisv1.MetricsTapErrorEvent{
			Code:      TapErrorCodeAuthorization,
			Message:   "could not verify access to the selected metrics",
			Retryable: true,
		}
	}
	return nil
}

// eventTime is the form timestamps are sent in: UTC, with millisecond precision.
func eventTime(t time.Time) time.Time {
	return t.UTC().Truncate(time.Millisecond)
}

func familyType(family *dto.MetricFamily) string {
	return strings.ToLower(family.GetType().String())
}

// describeFamilies builds the catalog of the families of a scrape, which are sorted by name
// already for every target group but the backend itself.
func describeFamilies(families []*dto.MetricFamily) []apisv1.MetricFamilyInfo {
	infos := make([]apisv1.MetricFamilyInfo, 0, len(families))
	for _, family := range families {
		labelNames := make(map[string]bool)
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				labelNames[label.GetName()] = true
			}
		}
		// Never nil, so that a family without labels is described with [] and not with null.
		names := make([]string, 0, len(labelNames))
		for name := range labelNames {
			names = append(names, name)
		}
		sort.Strings(names)
		infos = append(infos, apisv1.MetricFamilyInfo{
			Name:        family.GetName(),
			Type:        familyType(family),
			Help:        family.GetHelp(),
			LabelNames:  names,
			SeriesCount: len(family.GetMetric()),
		})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos
}

// flattenFamilies extracts the samples of the families named in metrics, in that order. A family
// the target does not expose is left out. Histograms and summaries are flattened to their
// _bucket, _sum and _count series, and to their quantiles. At most maxSamples samples are
// returned, the second result reporting whether some were left out.
func flattenFamilies(result *scrapeResult, metrics []string, maxSamples int) ([]apisv1.MetricFamilySamples, bool) {
	families := make([]apisv1.MetricFamilySamples, 0, len(metrics))
	total := 0
	for _, name := range metrics {
		family, ok := result.byName[name]
		if !ok {
			continue
		}
		// The options must not be nil: ExtractSamples dereferences them. A family of a type
		// it does not know yields no sample, which is what is reported.
		vector, _ := expfmt.ExtractSamples(&expfmt.DecodeOptions{}, family)
		truncated := false
		if total+len(vector) > maxSamples {
			vector = vector[:maxSamples-total]
			truncated = true
		}
		total += len(vector)
		samples := make([]apisv1.MetricSample, 0, len(vector))
		for _, sample := range vector {
			s := apisv1.MetricSample{Value: sample.Value.String()}
			for labelName, labelValue := range sample.Metric {
				if labelName == model.MetricNameLabel {
					if string(labelValue) != name {
						s.Name = string(labelValue)
					}
					continue
				}
				if s.Labels == nil {
					s.Labels = make(map[string]string, len(sample.Metric)-1)
				}
				s.Labels[string(labelName)] = string(labelValue)
			}
			samples = append(samples, s)
		}
		families = append(families, apisv1.MetricFamilySamples{
			Name:    name,
			Type:    familyType(family),
			Samples: samples,
		})
		if truncated {
			return families, true
		}
	}
	return families, false
}
