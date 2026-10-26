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

import { html, css, nothing } from 'lit';
import { state, property } from 'lit/decorators.js';
import { pageStyles, pickerStyles } from '../lib/styles.js';
import { apiFetchJSON, APIError } from '../lib/api.js';
import {
    accessSummary, namespaceAccessSummaries, can, pageVerdict,
    GATE_NAMESPACES_LIST, GATE_PODS_LIST, GATE_SERVICES_LIST,
    GATE_DEPLOYMENTS_LIST, GATE_STATEFULSETS_LIST, GATE_DAEMONSETS_LIST,
    GATE_K8S_NETWORKPOLICIES_LIST, GATE_ANTREA_CLUSTERNETWORKPOLICIES_LIST, GATE_ANTREA_NETWORKPOLICIES_LIST,
    GATE_EVENTS_LIST,
} from '../lib/access-api.js';
import type { HasRules, ResourceQuery } from '../lib/access-api.js';
import { namespaceAccess, mayNameNamespace, scopeNote, isValidNamespaceName } from '../lib/namespace-access.js';
import type { NamespaceAccess, ScopeNote } from '../lib/namespace-access.js';
import { SCOPE_ALL, parseScopeParam, dispatchScopeChosen, writeScopeToURL } from '../lib/namespace-scope.js';
import type { ScopeSelectDetail } from '../antrea-namespace-picker.js';
import { SessionAwarePage } from '../lib/session-aware-page.js';
import '../antrea-card';
import '../antrea-alert';
import '../antrea-namespace-picker';

// ── Types ──────────────────────────────────────────────────────────────────

interface K8sItem { metadata: { name: string; namespace?: string } }
interface K8sList {
    items: K8sItem[];
    metadata?: {
        /** Set when the server truncated the list at the request's `limit`: there are more items. */
        continue?: string;
        /** How many items were left off the page. The API server sets it on a truncated list
         * whenever it can compute it, which lets a tile show the true total from one request
         * instead of a "N+" floor; it is absent when the count is not available. */
        remainingItemCount?: number;
    };
}

// Mirrors the fields this page reads from a core/v1 Event. Cast into from the generic K8sItem
// fetch below rather than given its own RESOURCES/apiFetchJSON type parameter — the shape only
// matters at the one call site that reads events, same as the ad hoc pod/service field reads it
// replaces.
interface K8sEvent {
    metadata: { name: string; namespace?: string };
    involvedObject: { kind?: string; namespace?: string; name?: string };
    reason?: string;
    message?: string;
    type?: string;
    firstTimestamp?: string;
    lastTimestamp?: string;
    eventTime?: string;
    /** Set by events.k8s.io/v1 sources for a repeating event; its lastObservedTime is the latest
     * occurrence, while eventTime stays at the first. */
    series?: { lastObservedTime?: string };
}

/** The ISO timestamp to sort an event by, in order of preference: series.lastObservedTime (latest
 * occurrence of a repeating events.k8s.io/v1 event, whose lastTimestamp is empty and eventTime is
 * the first occurrence), lastTimestamp (the field most events set, bumped on every repeat),
 * eventTime, and firstTimestamp (the fallback for an event with none of those). Falls back to
 * '', which a descending sort puts last. */
function eventTimestamp(ev: K8sEvent): string {
    return ev.series?.lastObservedTime || ev.lastTimestamp || ev.eventTime || ev.firstTimestamp || '';
}

function formatEventTime(ev: K8sEvent): string {
    const ts = eventTimestamp(ev);
    if (!ts) return 'Unknown';
    return new Date(ts).toLocaleString();
}

interface ResourceSpec {
    key: string;
    /** The tile's heading, and how the resource is named in error messages. */
    label: string;
    gate: ResourceQuery;
    /** The resource is cluster-scoped, so a namespace never narrows it. It is only fetched, and
     * only shown, while All Namespaces is selected: with a namespace selected the gate would be
     * evaluated against that namespace's summary, which can hold `list` on it through a
     * RoleBinding without authorizing it cluster-wide, and the request would only come back 403. */
    clusterScoped?: boolean;
    /** `limit` sent on the list request. See COUNT_LIMIT and EVENTS_LIMIT. */
    limit: number;
    /** Builds the k8s proxy path (see pkg/server/api/k8s.go — RBAC is the only guard, there is no
     * path allowlist), given the selected namespace ('' meaning all namespaces / cluster scope). */
    path: (ns: string) => string;
}

// How many of the most recent events to show. This is a dashboard glance, not an events viewer:
// deliberately smaller than EVENTS_LIMIT, which bounds the fetch, not the display.
const EVENTS_DISPLAY_LIMIT = 20;

// `limit` sent on every list request. Without it the API server returns every object of the
// kind, so counting Pods on a large cluster would pull tens of MB of Pod specs through the proxy
// on every load of what is the landing page, and a namespace switch would repeat it. The count
// stays exact as long as the server reports metadata.remainingItemCount, which makes a page of any
// size give the same total as items.length + remainingItemCount; if a server ever omits it, the
// tile shows "N+" instead.
//
// A tile only needs the count, so its page is small. Events are the one kind whose page contents
// are used, and they have a limit of their own for that reason.
const COUNT_LIMIT = 100;
// The Recent Events rows are the newest of this page. A limited list comes back in storage-key
// (namespace/name) order, not time order, so on a cluster with more events than this the newest
// are not guaranteed to be on it, and the card says so. 2000 is what Headlamp fetches for its
// Overview: a larger request than the other kinds, but the newest events are more likely to be in
// it.
const EVENTS_LIMIT = 2000;

/** Appends a page limit to a resource path. The proxy forwards the query string untouched (see
 * pkg/server/api/k8s.go, which only rewrites the path). */
function withPageLimit(path: string, limit: number): string {
    return `${path}?limit=${limit}`;
}

const RESOURCES: ResourceSpec[] = [
    { key: 'namespaces', label: 'Namespaces', gate: GATE_NAMESPACES_LIST,
        clusterScoped: true, limit: COUNT_LIMIT, path: () => 'k8s/api/v1/namespaces' },
    { key: 'pods', label: 'Pods', gate: GATE_PODS_LIST,
        limit: COUNT_LIMIT,
        path: ns => ns ? `k8s/api/v1/namespaces/${ns}/pods` : 'k8s/api/v1/pods' },
    { key: 'services', label: 'Services', gate: GATE_SERVICES_LIST,
        limit: COUNT_LIMIT,
        path: ns => ns ? `k8s/api/v1/namespaces/${ns}/services` : 'k8s/api/v1/services' },
    { key: 'deployments', label: 'Deployments', gate: GATE_DEPLOYMENTS_LIST,
        limit: COUNT_LIMIT,
        path: ns => ns ? `k8s/apis/apps/v1/namespaces/${ns}/deployments` : 'k8s/apis/apps/v1/deployments' },
    { key: 'statefulsets', label: 'StatefulSets', gate: GATE_STATEFULSETS_LIST,
        limit: COUNT_LIMIT,
        path: ns => ns ? `k8s/apis/apps/v1/namespaces/${ns}/statefulsets` : 'k8s/apis/apps/v1/statefulsets' },
    { key: 'daemonsets', label: 'DaemonSets', gate: GATE_DAEMONSETS_LIST,
        limit: COUNT_LIMIT,
        path: ns => ns ? `k8s/apis/apps/v1/namespaces/${ns}/daemonsets` : 'k8s/apis/apps/v1/daemonsets' },
    { key: 'k8sNetworkPolicies', label: 'K8s Network Policies', gate: GATE_K8S_NETWORKPOLICIES_LIST,
        limit: COUNT_LIMIT,
        path: ns => ns ? `k8s/apis/networking.k8s.io/v1/namespaces/${ns}/networkpolicies` : 'k8s/apis/networking.k8s.io/v1/networkpolicies' },
    { key: 'antreaClusterNetworkPolicies', label: 'Antrea Cluster Network Policies', gate: GATE_ANTREA_CLUSTERNETWORKPOLICIES_LIST,
        clusterScoped: true, limit: COUNT_LIMIT, path: () => 'k8s/apis/crd.antrea.io/v1beta1/clusternetworkpolicies' },
    { key: 'antreaNetworkPolicies', label: 'Antrea Network Policies', gate: GATE_ANTREA_NETWORKPOLICIES_LIST,
        limit: COUNT_LIMIT,
        path: ns => ns ? `k8s/apis/crd.antrea.io/v1beta1/namespaces/${ns}/networkpolicies` : 'k8s/apis/crd.antrea.io/v1beta1/networkpolicies' },
    { key: 'events', label: 'Events', gate: GATE_EVENTS_LIST, limit: EVENTS_LIMIT,
        path: ns => ns ? `k8s/api/v1/namespaces/${ns}/events` : 'k8s/api/v1/events' },
];

// The tiles, in display order. Each takes its heading from its RESOURCES entry, so a resource is
// named the same on its tile and in an error message. Events are not here: they back the "Recent
// Events" panel below, not a count tile.
const TILES: string[] = [
    'namespaces', 'pods', 'services', 'deployments', 'statefulsets', 'daemonsets',
    'k8sNetworkPolicies', 'antreaNetworkPolicies', 'antreaClusterNetworkPolicies',
];

// The permissions the page shows, from which its cluster-wide verdict is made (rule 4 of
// docs/design/namespace-scope-ux.md): All Namespaces is offered unless it is 'denied', and is the
// default only when it is 'allowed'. It says nothing once a Namespace is selected: the tiles are
// then gated on that Namespace's own summary (see _summaryForScope).
const PAGE_GATES: ResourceQuery[] = RESOURCES.map(r => r.gate);

// Shown while no scope is selected: the page has nothing to show until one is.
const SELECT_SCOPE_MESSAGE = 'Select a namespace to see its overview.';
const UNKNOWN_NAMESPACES_NOTE =
    'The picker has no complete list of namespaces to offer: it is empty, could not be loaded, ' +
    'or was cut short. Use the "Namespace not listed" box to name one directly.';

// ── Component ────────────────────────────────────────────────────────────────

export class AntreaOverviewPage extends SessionAwarePage {
    static styles = [pageStyles, pickerStyles, css`
        .stat-grid {
            display: grid;
            grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
            gap: var(--antrea-space-md, 1rem);
        }
        /* Every tile is the height of the tallest one in its row, so a two-line heading like
           "K8s Network Policies" doesn't leave its neighbours short. Grid items already stretch;
           this passes that height down to antrea-card's internal wrapper. */
        .stat-grid antrea-card { height: 100%; }
        .stat-value {
            font-size: 2rem;
            font-weight: var(--antrea-font-weight-bold, 600);
            color: var(--antrea-color-primary, #0079b8);
            text-align: center;
        }
        /* A re-filter leaves the previous numbers on screen; dim them so they don't read as
           current while the new ones are still loading. */
        .stale { opacity: 0.5; }
        .event-type-warning { color: var(--antrea-color-warning, #f5a623); }
    `];

    // The selected scope: SCOPE_ALL, a Namespace name, or '' for none.
    @state() private _scope = '';
    @state() private _loading = true;
    // Set while a re-filter is in flight. Unlike _loading it does not replace the page with a
    // spinner; the previous tiles stay visible, dimmed.
    @state() private _refreshing = false;
    @state() private _counts: Record<string, number> = {};
    @state() private _truncated: Record<string, boolean> = {};
    @state() private _events: K8sEvent[] = [];
    // The events list was cut off at EVENTS_LIMIT, so _events is the newest of a key-ordered prefix.
    @state() private _eventsTruncated = false;
    // See antrea-summary-page.ts for why these two are tracked (and reported) separately: one
    // means "the UI is hiding something the user isn't allowed to see", the other means "a call
    // the user IS allowed to make failed for some other reason".
    @state() private _someCardsForbidden = false;
    @state() private _cardErrors: string[] = [];
    // Every resource the selected scope was asked for was either gated off or came back 403, and
    // nothing failed otherwise: the user has nothing to see here, which is a different thing from
    // "some of it is not shown". See _loadCounts().
    @state() private _nothingPermitted = false;
    // Why the scope is not what the user asked for: the URL carries an invalid one, or a name
    // they typed is not a valid Namespace name. Reported on the page.
    @state() private _scopeError: string | null = null;
    // A note on the selected namespace: that it is not in a complete list.
    @state() private _scopeNote: ScopeNote | null = null;
    private _scopeNoteToken = 0;

    /** The page URL's query string, passed by a host whose router owns the location, so the page
     * follows it when it changes (Back, Forward, or switching between the page's own views). A
     * page without a host reads window.location instead. */
    @property() locationSearch?: string;

    // What the picker offers: the Namespace list, and the page's cluster-wide verdict. Null until
    // it resolves; a fetch that fails resolves to an answer with no candidates, so the page is
    // never left without a way to choose a scope.
    @state() private _access: NamespaceAccess | null = null;
    // The query string of the URL as the page last took it, whether it wrote it or read it, so
    // that the host handing back what the page wrote is not mistaken for a navigation.
    private _appliedSearch = '';
    private _sessionStarted = false;
    // The scope the page last loaded, so that taking the same one again (the Namespace list
    // arriving after a scope from the URL was already loaded) does not load it twice.
    private _loadedScope = '';
    // Whether a load has already completed, so a re-filter can keep the page rendered.
    private _loadedOnce = false;
    // Bumped before each _loadCounts() and captured per-call, so a response from a superseded
    // call (e.g. the namespace filter changed again before the first fetch returned) can't
    // overwrite state after a newer one has already resolved.
    private _loadGeneration = 0;

    private _onPopState = () => {
        if (this.locationSearch === undefined) this._onLocationChange();
    };

    override connectedCallback() {
        super.connectedCallback();
        window.addEventListener('popstate', this._onPopState);
    }

    override disconnectedCallback() {
        super.disconnectedCallback();
        window.removeEventListener('popstate', this._onPopState);
    }

    override willUpdate(changed: Map<string, unknown>) {
        super.willUpdate(changed);
        if (changed.has('locationSearch')) this._onLocationChange();
    }

    override firstUpdated(changed: Map<string, unknown>) {
        super.firstUpdated(changed);
        // The scope taken from the URL at mount was dispatched before the host could listen: the
        // element is connected before its listeners are attached. Say it again once mounted.
        if (this._sessionStarted) {
            const parsed = parseScopeParam(this._appliedSearch);
            if (parsed.kind === 'scope') dispatchScopeChosen(this, parsed.value);
        }
    }

    protected override onSessionReady() {
        // A scope the URL carries is used as it is, so its content loads at once while the
        // namespace list loads for the picker. Only a default, when the URL has none, has to wait
        // for the list and the verdict: it is applied when they arrive (see _loadAccess).
        this._appliedSearch = this._search;
        this._sessionStarted = true;
        this._applyScopeFromURL();
        void this._loadAccess();
    }

    /** Loads the picker's options, then settles on a default scope if the URL named none. */
    private async _loadAccess() {
        // A dead session shows here first when nothing else is fetched (no scope to load yet).
        const summary = await accessSummary().catch(err => {
            if (this.isSessionExpiredError(err)) this.dispatchSessionExpired();
            return null;
        });
        const access = await namespaceAccess(summary, s => pageVerdict(s, PAGE_GATES));
        // The page may have been left while this loaded: going on would load content nothing
        // will show.
        if (!this.isConnected) return;
        this._access = access;
        // The URL the page last took or wrote, not the location prop: a scope picked while the
        // list loaded is in the former, and the router may not have delivered it to the latter.
        this._applyScopeFromURL(this._appliedSearch);
    }

    /** The query string the page's scope is read from. */
    private get _search(): string {
        return this.locationSearch ?? window.location.search;
    }

    /** Takes the scope from the URL again when the URL has changed: Back or Forward, or a switch
     * between this page's own views. The page's own writes do not count (see _appliedSearch). */
    private _onLocationChange() {
        const search = this._search;
        if (!this._sessionStarted || search === this._appliedSearch) return;
        this._appliedSearch = search;
        this._applyScopeFromURL(search);
    }

    /**
     * Decides the scope from the URL, or else from the default order, and loads it. Runs when the
     * page opens, when its URL changes, and when the namespace list arrives.
     *
     * A scope in the URL is used as given, with no fallback: not checked against the list, and
     * not replaced by a default even when it is All Namespaces and the page's verdict is denied.
     * An invalid one is reported and nothing is selected.
     */
    private _applyScopeFromURL(search: string = this._search) {
        const parsed = parseScopeParam(search);
        if (parsed.kind === 'invalid') {
            this._scopeError = parsed.message;
            this._setScope('');
        } else if (parsed.kind === 'scope') {
            this._scopeError = null;
            this._setScope(parsed.value);
            dispatchScopeChosen(this, parsed.value);
        } else {
            this._scopeError = null;
            // The default needs the list and the verdict, so it waits for them. Nothing is
            // selected until then, and nothing is written to the URL: a default is not a choice.
            this._setScope(this._access ? this._defaultScope() : '');
        }
        void this._refreshScopeNote();
    }

    /**
     * The scope the page opens with when the URL names none, or '' for none: All Namespaces only
     * where the page's verdict is allowed (an unknown verdict never makes it the default);
     * otherwise the first namespace of the list, without checking it. A namespace's access is
     * only looked at once it is selected, and decides which tiles are shown.
     */
    private _defaultScope(): string {
        if (this._access?.clusterWide === 'allowed') return SCOPE_ALL;
        return this._access?.candidates?.[0] ?? '';
    }

    /** Sets the scope and loads its content, unless it is the one already loaded. */
    private _setScope(scope: string) {
        this._scope = scope;
        if (scope === this._loadedScope) return;
        if (scope === '') {
            // Nothing to show: drop what the previous scope loaded.
            this._loadGeneration++;
            this._loadedScope = '';
            this._loadedOnce = false;
            this._counts = {};
            this._truncated = {};
            this._events = [];
            this._eventsTruncated = false;
            this._someCardsForbidden = false;
            this._cardErrors = [];
            this._nothingPermitted = false;
            this._loading = false;
            this._refreshing = false;
            return;
        }
        void this._loadCounts();
    }

    /** Notes that the selected namespace is not in the list (see scopeNote). */
    private async _refreshScopeNote() {
        const token = ++this._scopeNoteToken;
        this._scopeNote = null;
        const ns = this._scope === SCOPE_ALL ? '' : this._scope;
        const note = await scopeNote(this._access, ns, () => token === this._scopeNoteToken && this.isConnected);
        if (token === this._scopeNoteToken) this._scopeNote = note;
    }

    /** What to gate on for a scope. For All Namespaces, the cluster-scoped summary. For a
     * namespace, a summary for it: it is needed to see grants that come from a RoleBinding in it,
     * and it is the per-namespace summary rather than a memoized one for good, so that a grant
     * added while the page is open shows up within about a minute (see namespaceAccessSummaries).
     * Fails open (null) when it cannot be fetched, and an item whose review failed carries
     * `incomplete`, which does the same. */
    private async _summaryForScope(ns: string): Promise<HasRules | null> {
        if (!ns) return accessSummary().catch(() => null);
        try {
            return (await namespaceAccessSummaries([ns])).items[0] ?? null;
        } catch {
            return null;
        }
    }

    /** The user chose a scope: a pick, or a name they typed. A name that is not a valid Namespace
     * name is rejected and the scope stays as it was. */
    private _onScopeSelect(value: string) {
        if (value !== SCOPE_ALL && !isValidNamespaceName(value)) {
            this._scopeError = `"${value}" is not a valid namespace name.`;
            return;
        }
        this._scopeError = null;
        dispatchScopeChosen(this, value);
        // Chosen again, or chosen where the page was defaulted: the choice is still written, so
        // that a reload keeps it as one.
        this._appliedSearch = writeScopeToURL(this, value);
        if (value === this._scope) return;
        this._setScope(value);
        void this._refreshScopeNote();
    }

    private async _loadCounts() {
        const generation = ++this._loadGeneration;
        // Only the very first load replaces the page with a spinner. A namespace re-filter keeps
        // the previous tiles visible and just marks them stale.
        const scope = this._scope;
        this._loadedScope = scope;
        if (this._loadedOnce) this._refreshing = true;
        else this._loading = true;
        this._someCardsForbidden = false;
        this._cardErrors = [];
        // Reset with the other two: the scope has already changed by now, and the stale page
        // would otherwise tell the user they may see nothing in the namespace they just picked.
        this._nothingPermitted = false;
        const namespace = scope === SCOPE_ALL ? '' : scope;
        const summary = await this._summaryForScope(namespace);
        if (generation !== this._loadGeneration) return;

        // A cluster-scoped resource is not asked about while a namespace is selected: see
        // ResourceSpec.clusterScoped. It is skipped outright, so it counts neither as hidden from
        // the user nor as something that could have been shown.
        const skipped = (r: ResourceSpec) => Boolean(r.clusterScoped && namespace);
        const results = await Promise.allSettled(RESOURCES.map(r =>
            !skipped(r) && can(summary, r.gate)
                ? apiFetchJSON<K8sList>(withPageLimit(r.path(namespace), r.limit))
                : Promise.reject(new Error('skipped: no permission')),
        ));
        if (generation !== this._loadGeneration) return;

        for (const result of results) {
            if (result.status === 'rejected' && this.isSessionExpiredError(result.reason)) {
                this.dispatchSessionExpired();
                return;
            }
        }

        const counts: Record<string, number> = {};
        const truncated: Record<string, boolean> = {};
        // Whether anything was shown or failed for a reason other than permission.
        let anythingShown = false;
        let events: K8sEvent[] = [];
        let eventsTruncated = false;

        RESOURCES.forEach((r, i) => {
            if (skipped(r)) return;
            if (!can(summary, r.gate)) {
                this._someCardsForbidden = true;
                return;
            }
            const result = results[i];
            if (result.status === 'fulfilled') {
                anythingShown = true;
                // A page plus the count of what was left off it is the true total. Only when the
                // server truncated without reporting that remainder is the count a floor.
                const remaining = result.value.metadata?.remainingItemCount;
                counts[r.key] = result.value.items.length + (remaining ?? 0);
                truncated[r.key] = Boolean(result.value.metadata?.continue) && remaining === undefined;
                if (r.key === 'events') {
                    // Newest first, then capped for display — EVENTS_LIMIT bounds the fetch, not
                    // what's shown. The page is sorted client-side because the API returns it in
                    // key order, which also means a truncated page may lack the newest events.
                    eventsTruncated = Boolean(result.value.metadata?.continue);
                    events = (result.value.items as unknown as K8sEvent[])
                        .slice()
                        .sort((a, b) => eventTimestamp(b).localeCompare(eventTimestamp(a)))
                        .slice(0, EVENTS_DISPLAY_LIMIT);
                }
            } else {
                this._recordFailure(r.label, result.reason);
                // A 403 is not "shown" and not an error; anything else is already explained by
                // the danger alert, and is not a permissions problem.
                if (!(result.reason instanceof APIError && result.reason.code === 403)) anythingShown = true;
            }
        });
        this._nothingPermitted = !anythingShown;

        this._counts = counts;
        this._truncated = truncated;
        this._events = events;
        this._eventsTruncated = eventsTruncated;
        this._loading = false;
        this._refreshing = false;
        this._loadedOnce = true;
    }

    /** Classifies a failed fetch the gate had allowed. A 403 means the access summary was wrong
     * (stale, incomplete, or fail-open) and the resource really is forbidden; anything else is a
     * genuine error and must be surfaced as one, with its message. */
    private _recordFailure(label: string, reason: unknown) {
        if (reason instanceof APIError && reason.code === 403) {
            this._someCardsForbidden = true;
            return;
        }
        const message = reason instanceof Error ? reason.message : String(reason);
        this._cardErrors = [...this._cardErrors, `${label}: ${message}`];
    }

    private _renderTile(key: string) {
        if (!(key in this._counts)) return '';
        const label = RESOURCES.find(r => r.key === key)!.label;
        return html`
            <antrea-card heading=${label}>
                <div class="stat-value">${this._counts[key]}${this._truncated[key] ? '+' : ''}</div>
            </antrea-card>
        `;
    }

    private _renderEvents() {
        // Absent only when the user may not list events. A request that succeeded and found none
        // is an answer ("nothing happened": events expire after an hour by default), and a missing
        // card would read as a missing panel instead.
        if (!('events' in this._counts)) return '';
        if (this._events.length === 0) {
            return html`
                <antrea-card heading="Recent Events">
                    <p class="text-muted">No recent events</p>
                </antrea-card>
            `;
        }
        return html`
            <antrea-card heading="Recent Events">
                ${this._eventsTruncated ? html`
                    <antrea-alert status="info">
                        This cluster has more events than can be fetched at once; the newest may not be listed.
                    </antrea-alert>
                ` : ''}
                <table class="data-table" part="table">
                    <thead>
                        <tr>
                            <th part="table-header-cell">Last Seen</th>
                            <th part="table-header-cell">Type</th>
                            <th part="table-header-cell">Object</th>
                            <th part="table-header-cell">Reason</th>
                            <th part="table-header-cell">Message</th>
                        </tr>
                    </thead>
                    <tbody>
                        ${this._events.map(ev => html`
                            <tr>
                                <td part="table-cell">${formatEventTime(ev)}</td>
                                <td part="table-cell" class=${ev.type === 'Warning' ? 'event-type-warning' : ''}>${ev.type ?? 'Unknown'}</td>
                                <td part="table-cell">${ev.involvedObject.kind ?? 'Unknown'} ${ev.involvedObject.namespace ? `${ev.involvedObject.namespace}/` : ''}${ev.involvedObject.name ?? ''}</td>
                                <td part="table-cell">${ev.reason ?? ''}</td>
                                <td part="table-cell">${ev.message ?? ''}</td>
                            </tr>
                        `)}
                    </tbody>
                </table>
            </antrea-card>
        `;
    }

    private _renderScopeSelect() {
        return html`
            <div class="row">
                <antrea-namespace-picker
                    label="Namespace" inputId="ns-picker"
                    .access=${this._access}
                    .scope=${this._scope}
                    .offerAll=${(this._access?.clusterWide ?? 'unknown') !== 'denied'}
                    .note=${this._scopeNote?.text ?? ''}
                    @antrea-scope-select=${(e: CustomEvent<ScopeSelectDetail>) => this._onScopeSelect(e.detail.value)}>
                </antrea-namespace-picker>
            </div>
        `;
    }

    /** What the page shows while no scope is selected: an explanation, and the picker above it is
     * the way out (including the box to name a namespace, when the list cannot be relied on). */
    private _renderNoScope() {
        if (!this._access && !this._scopeError) {
            return html`<div class="loading-row"><span class="spinner"></span><span>Loading…</span></div>`;
        }
        return html`
            <p class="text-muted">${SELECT_SCOPE_MESSAGE}</p>
            ${mayNameNamespace(this._access) ? html`<p class="text-muted">${UNKNOWN_NAMESPACES_NOTE}</p>` : nothing}
        `;
    }

    override render() {
        const namespace = this._scope === SCOPE_ALL ? '' : this._scope;
        return html`
            <main>
                <div class="page-layout">
                    ${this._scopeError ? html`<antrea-alert status="danger">${this._scopeError}</antrea-alert>` : nothing}
                    ${this._nothingPermitted ? html`
                        <antrea-alert status="warning">
                            ${this._scopeNote?.kind === 'missing'
                            // For a user whose grants come from RoleBindings, every tile is denied
                            // for a Namespace that does not exist, as the access summary does not
                            // check existence, so "no permission" would give the wrong reason.
                            ? this._scopeNote.text
                            : html`Your account does not have permission to view any of this information
                            ${namespace ? html`in namespace ${namespace}` : 'cluster-wide'}.`}
                        </antrea-alert>
                    ` : this._someCardsForbidden ? html`
                        <antrea-alert status="info">
                            Some information is not shown because your account does not have permission to view it.
                        </antrea-alert>
                    ` : nothing}
                    ${this._cardErrors.length > 0 ? html`
                        <antrea-alert status="danger">
                            ${this._cardErrors.join('; ')}
                        </antrea-alert>
                    ` : nothing}

                    ${this._renderScopeSelect()}

                    ${this._scope === '' ? this._renderNoScope()
                    : this._loading ? html`<div class="loading-row"><span class="spinner"></span><span>Loading…</span></div>`
                    : html`
                        <div class="stat-grid ${this._refreshing ? 'stale' : ''}">
                            ${TILES.map(tile => this._renderTile(tile))}
                        </div>

                        <div class=${this._refreshing ? 'stale' : ''}>
                            ${this._renderEvents()}
                        </div>
                    `}
                </div>
            </main>
        `;
    }
}

customElements.define('antrea-overview-page', AntreaOverviewPage);

declare global {
    interface HTMLElementTagNameMap { 'antrea-overview-page': AntreaOverviewPage; }
}
