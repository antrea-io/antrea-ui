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

import { html, css } from 'lit';
import { state } from 'lit/decorators.js';
import { pageStyles } from '../lib/styles.js';
import { apiFetchJSON, APIError } from '../lib/api.js';
import {
    accessSummary, accessibleNamespaces, namespaceAccessSummaries, can, verdict,
    MAX_NAMESPACE_ACCESS_NAMES,
    GATE_NAMESPACES_LIST, GATE_PODS_LIST, GATE_SERVICES_LIST,
    GATE_DEPLOYMENTS_LIST, GATE_STATEFULSETS_LIST, GATE_DAEMONSETS_LIST,
    GATE_K8S_NETWORKPOLICIES_LIST, GATE_ANTREA_CLUSTERNETWORKPOLICIES_LIST, GATE_ANTREA_NETWORKPOLICIES_LIST,
    GATE_EVENTS_LIST,
} from '../lib/access-api.js';
import type { AccessSummary, ResourceQuery } from '../lib/access-api.js';
import { SessionAwarePage } from '../lib/session-aware-page.js';
import '../antrea-card';
import '../antrea-alert';

// ── Types ──────────────────────────────────────────────────────────────────

interface K8sItem { metadata: { name: string; namespace?: string } }
interface K8sList {
    items: K8sItem[];
    metadata?: {
        /** Set when the server truncated the list at PAGE_LIMIT: there are more items. */
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
    /** Used only in error messages — see TILES below for what's actually displayed. */
    label: string;
    gate: ResourceQuery;
    /** Builds the k8s proxy path (see pkg/server/api/k8s.go — RBAC is the only guard, there is no
     * path allowlist), given the selected namespace ('' meaning all namespaces / cluster scope). */
    path: (ns: string) => string;
}

interface Tile {
    label: string;
    resourceKey: string;
    /** The resource is cluster-scoped, so the namespace filter never narrows it. The tile says so
     * while a namespace is selected, rather than sitting among namespaced counts unlabelled. */
    clusterScoped?: boolean;
}

// How many of the most recent events to show. This is a dashboard glance, not an events viewer:
// deliberately smaller than PAGE_LIMIT, which bounds the fetch, not the display.
const EVENTS_DISPLAY_LIMIT = 20;

// `limit` sent on every list request. Without it the API server returns every object of the
// kind, so counting Pods on a large cluster would pull tens of MB of Pod specs through the proxy
// on every load of what is the landing page — and the truncation handling below would be dead
// code, since an unlimited list is never truncated. The count stays exact as long as the server
// reports metadata.remainingItemCount; otherwise the tile shows "N+". Kept comfortably above
// EVENTS_DISPLAY_LIMIT so a full page of events is available to sort. A limited list comes back in
// storage-key (namespace/name) order, not time order, so on a cluster with more than PAGE_LIMIT
// events the newest are not guaranteed to be on the page; the Recent Events card says so.
const PAGE_LIMIT = 500;

/** Appends PAGE_LIMIT to a resource path. The proxy forwards the query string untouched (see
 * pkg/server/api/k8s.go, which only rewrites the path). */
function withPageLimit(path: string): string {
    return `${path}?limit=${PAGE_LIMIT}`;
}

const RESOURCES: ResourceSpec[] = [
    // The namespace <select> is built from this page, so a cluster with more than PAGE_LIMIT
    // namespaces gets a truncated picker (the tile still shows the true total). Namespaces are
    // the one kind where that trade-off bites; it beats an unbounded list on every other.
    { key: 'namespaces', label: 'Namespaces', gate: GATE_NAMESPACES_LIST,
        path: () => 'k8s/api/v1/namespaces' },
    { key: 'pods', label: 'Pods', gate: GATE_PODS_LIST,
        path: ns => ns ? `k8s/api/v1/namespaces/${ns}/pods` : 'k8s/api/v1/pods' },
    { key: 'services', label: 'Services', gate: GATE_SERVICES_LIST,
        path: ns => ns ? `k8s/api/v1/namespaces/${ns}/services` : 'k8s/api/v1/services' },
    { key: 'deployments', label: 'Deployments', gate: GATE_DEPLOYMENTS_LIST,
        path: ns => ns ? `k8s/apis/apps/v1/namespaces/${ns}/deployments` : 'k8s/apis/apps/v1/deployments' },
    { key: 'statefulsets', label: 'StatefulSets', gate: GATE_STATEFULSETS_LIST,
        path: ns => ns ? `k8s/apis/apps/v1/namespaces/${ns}/statefulsets` : 'k8s/apis/apps/v1/statefulsets' },
    { key: 'daemonsets', label: 'DaemonSets', gate: GATE_DAEMONSETS_LIST,
        path: ns => ns ? `k8s/apis/apps/v1/namespaces/${ns}/daemonsets` : 'k8s/apis/apps/v1/daemonsets' },
    { key: 'k8sNetworkPolicies', label: 'K8s Network Policies', gate: GATE_K8S_NETWORKPOLICIES_LIST,
        path: ns => ns ? `k8s/apis/networking.k8s.io/v1/namespaces/${ns}/networkpolicies` : 'k8s/apis/networking.k8s.io/v1/networkpolicies' },
    // ClusterNetworkPolicy is cluster-scoped (no namespace concept) — the namespace filter never
    // narrows it.
    { key: 'antreaClusterNetworkPolicies', label: 'Antrea ClusterNetworkPolicies', gate: GATE_ANTREA_CLUSTERNETWORKPOLICIES_LIST,
        path: () => 'k8s/apis/crd.antrea.io/v1beta1/clusternetworkpolicies' },
    { key: 'antreaNetworkPolicies', label: 'Antrea NetworkPolicies', gate: GATE_ANTREA_NETWORKPOLICIES_LIST,
        path: ns => ns ? `k8s/apis/crd.antrea.io/v1beta1/namespaces/${ns}/networkpolicies` : 'k8s/apis/crd.antrea.io/v1beta1/networkpolicies' },
    // Not in TILES: events back the "Recent Events" panel below, not a count tile.
    { key: 'events', label: 'Events', gate: GATE_EVENTS_LIST,
        path: ns => ns ? `k8s/api/v1/namespaces/${ns}/events` : 'k8s/api/v1/events' },
];

const TILES: Tile[] = [
    { label: 'Namespaces', resourceKey: 'namespaces', clusterScoped: true },
    { label: 'Pods', resourceKey: 'pods' },
    { label: 'Services', resourceKey: 'services' },
    { label: 'Deployments', resourceKey: 'deployments' },
    { label: 'StatefulSets', resourceKey: 'statefulsets' },
    { label: 'DaemonSets', resourceKey: 'daemonsets' },
    { label: 'K8s Network Policies', resourceKey: 'k8sNetworkPolicies' },
    { label: 'Antrea Network Policies', resourceKey: 'antreaNetworkPolicies' },
    { label: 'Antrea ClusterNetworkPolicies', resourceKey: 'antreaClusterNetworkPolicies', clusterScoped: true },
];

// ── Component ────────────────────────────────────────────────────────────────

export class AntreaOverviewPage extends SessionAwarePage {
    static styles = [pageStyles, css`
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
        .scope-note {
            text-align: center;
            font-size: 0.75rem;
            color: var(--antrea-color-text-muted, #8a9aa8);
        }
        .event-type-warning { color: var(--antrea-color-warning, #f5a623); }
    `];

    @state() private _namespace = '';
    @state() private _namespaces: string[] = [];
    @state() private _loading = true;
    // Set while a re-filter is in flight. Unlike _loading it does not blank the page; the tiles
    // stay visible (dimmed) so the namespace <select> survives the update.
    @state() private _refreshing = false;
    @state() private _counts: Record<string, number> = {};
    @state() private _truncated: Record<string, boolean> = {};
    @state() private _events: K8sEvent[] = [];
    // The events list was cut off at PAGE_LIMIT, so _events is the newest of a key-ordered prefix.
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
    // Whether the cluster-scoped summary grants any resource this page shows, so that All
    // Namespaces has something to show. Without one the option can only lead to an empty page.
    @state() private _allNamespacesUseful = true;

    // Cluster-scoped: what the user may do cluster-wide. Gates the All Namespaces view.
    private _accessSummary: AccessSummary | null = null;
    // Namespaces from summary.namespaces, for a user who cannot list namespaces cluster-wide: the
    // picker is seeded from these as well as from the namespaces fetch.
    private _grantedNamespaces: string[] = [];
    // Whether a load has already completed, so a re-filter can keep the page rendered.
    private _loadedOnce = false;
    // Bumped before each _loadCounts() and captured per-call, so a response from a superseded
    // call (e.g. the namespace filter changed again before the first fetch returned) can't
    // overwrite state after a newer one has already resolved.
    private _loadGeneration = 0;

    protected override async onSessionReady() {
        let summary: AccessSummary | null;
        try {
            summary = await accessSummary();
        } catch {
            summary = null;
        }
        this._accessSummary = summary;
        this._grantedNamespaces = accessibleNamespaces(summary) ?? [];
        this._namespaces = this._grantedNamespaces.slice().sort();
        // Keeps namespaces in the check, unlike the one below: a user whose only cluster-wide
        // grant is "list namespaces" starts on All Namespaces, so hiding the option for them would
        // leave the <select> showing the first namespace while the page shows the cluster view,
        // and picking that namespace would fire no change event.
        this._allNamespacesUseful = RESOURCES.some(r => can(summary, r.gate));
        // A user with no cluster-wide list grant would see an empty All Namespaces view; start in
        // a namespace they have access to instead.
        if (this._grantedNamespaces.length > 0 && !RESOURCES.some(r => r.key !== 'namespaces' && can(summary, r.gate))) {
            this._namespace = await this._defaultNamespace();
        }
        await this._loadCounts();
    }

    /** The namespace to start in for a user with no cluster-wide grant: the first of theirs that
     * grants something this page shows. summary.namespaces is the RoleBinding-subject heuristic,
     * a superset of what the user can see, so its first entry may grant nothing here; the
     * per-namespace summaries say which do. Only the first MAX_NAMESPACE_ACCESS_NAMES are asked
     * about, since each is a review against the API server, and one that is merely unknown beats
     * one that is known to grant nothing. Fails open to the first, like every other gate. */
    private async _defaultNamespace(): Promise<string> {
        const candidates = this._namespaces.slice(0, MAX_NAMESPACE_ACCESS_NAMES);
        try {
            const { items } = await namespaceAccessSummaries(candidates);
            const verdictOf = (item: (typeof items)[number]) => {
                const verdicts = RESOURCES.filter(r => r.key !== 'namespaces').map(r => verdict(item, r.gate));
                return verdicts.includes('allowed') ? 'allowed' : verdicts.includes('unknown') ? 'unknown' : 'denied';
            };
            return (items.find(i => verdictOf(i) === 'allowed') ?? items.find(i => verdictOf(i) === 'unknown'))?.namespace
                ?? candidates[0];
        } catch {
            return candidates[0];
        }
    }

    /** The access summary to gate on for the current namespace filter. A namespaced summary is
     * needed to see grants that come from a RoleBinding in that namespace; fails open (null) when
     * it cannot be fetched, like every other gate. */
    private async _summaryForNamespace(ns: string): Promise<AccessSummary | null> {
        if (!ns) return this._accessSummary;
        try {
            return await accessSummary(ns);
        } catch {
            return null;
        }
    }

    private _onNamespaceChange(e: Event) {
        this._namespace = (e.target as HTMLSelectElement).value;
        this._loadCounts();
    }

    private async _loadCounts() {
        const generation = ++this._loadGeneration;
        // Only the very first load blanks the page for a spinner. A namespace re-filter keeps the
        // rendered page up and just marks it stale: tearing the page down mid-interaction would
        // destroy the <select> the user is interacting with, and rebuilding it resets its
        // selection (a fresh <select> has no options at the moment its value is assigned).
        if (this._loadedOnce) this._refreshing = true;
        else this._loading = true;
        this._someCardsForbidden = false;
        this._cardErrors = [];
        const namespace = this._namespace;
        const summary = await this._summaryForNamespace(namespace);
        if (generation !== this._loadGeneration) return;

        const results = await Promise.allSettled(RESOURCES.map(r =>
            can(summary, r.gate)
                ? apiFetchJSON<K8sList>(withPageLimit(r.path(namespace)))
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
        // Keeps the previous namespace list (and thus the dropdown) intact for a user who can't
        // list namespaces themselves but can read other resources scoped by one.
        let namespaces = this._namespaces;
        let events: K8sEvent[] = [];
        let eventsTruncated = false;

        RESOURCES.forEach((r, i) => {
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
                if (r.key === 'namespaces') {
                    namespaces = [...new Set([...result.value.items.map(it => it.metadata.name), ...this._grantedNamespaces])].sort();
                } else if (r.key === 'events') {
                    // Newest first, then capped for display — PAGE_LIMIT bounds the fetch, not
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
        this._namespaces = namespaces;
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

    private _renderTile(tile: Tile) {
        if (!(tile.resourceKey in this._counts)) return '';
        const showScopeNote = tile.clusterScoped && this._namespace !== '';
        return html`
            <antrea-card heading=${tile.label}>
                <div class="stat-value">${this._counts[tile.resourceKey]}${this._truncated[tile.resourceKey] ? '+' : ''}</div>
                ${showScopeNote ? html`<div class="scope-note">Cluster-wide</div>` : ''}
            </antrea-card>
        `;
    }

    private _renderEvents() {
        if (this._events.length === 0) return '';
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

    override render() {
        if (this._loading) {
            return html`<main><div class="loading-row"><span class="spinner"></span><span>Loading…</span></div></main>`;
        }

        return html`
            <main>
                <div class="page-layout">
                    ${this._nothingPermitted ? html`
                        <antrea-alert status="warning">
                            Your account does not have permission to view any of this information
                            ${this._namespace ? html`in namespace ${this._namespace}` : 'cluster-wide'}.
                        </antrea-alert>
                    ` : this._someCardsForbidden ? html`
                        <antrea-alert status="info">
                            Some information is not shown because your account does not have permission to view it.
                        </antrea-alert>
                    ` : ''}
                    ${this._cardErrors.length > 0 ? html`
                        <antrea-alert status="danger">
                            ${this._cardErrors.join('; ')}
                        </antrea-alert>
                    ` : ''}

                    ${this._namespaces.length > 0 ? html`
                        <div class="row">
                            <label class="field-label" for="ns-select">Namespace</label>
                            <!-- ?selected on each option rather than .value on the <select>: a
                                 property binding is committed while the option list may still be
                                 empty, which silently resets the selection to the first entry. -->
                            <select id="ns-select" class="field-select" style="max-width:240px" @change=${this._onNamespaceChange}>
                                ${this._allNamespacesUseful || this._namespace === '' ? html`
                                    <option value="" ?selected=${this._namespace === ''}>All Namespaces</option>
                                ` : ''}
                                ${this._namespaces.map(ns => html`<option value=${ns} ?selected=${ns === this._namespace}>${ns}</option>`)}
                            </select>
                        </div>
                    ` : ''}

                    <div class="stat-grid ${this._refreshing ? 'stale' : ''}">
                        ${TILES.map(tile => this._renderTile(tile))}
                    </div>

                    <div class=${this._refreshing ? 'stale' : ''}>
                        ${this._renderEvents()}
                    </div>
                </div>
            </main>
        `;
    }
}

customElements.define('antrea-overview-page', AntreaOverviewPage);

declare global {
    interface HTMLElementTagNameMap { 'antrea-overview-page': AntreaOverviewPage; }
}
