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
    accessSummary, can,
    GATE_NAMESPACES_LIST, GATE_PODS_LIST, GATE_SERVICES_LIST,
    GATE_DEPLOYMENTS_LIST, GATE_STATEFULSETS_LIST, GATE_DAEMONSETS_LIST,
    GATE_K8S_NETWORKPOLICIES_LIST, GATE_ANTREA_CLUSTERNETWORKPOLICIES_LIST, GATE_ANTREA_NETWORKPOLICIES_LIST,
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

interface NamedItem { name: string; namespace: string }

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
    /** Multiple keys are summed into one tile — used to combine Antrea's cluster-scoped and
     * namespaced NetworkPolicy kinds into a single "Antrea Network Policies" count. */
    resourceKeys: string[];
}

// How many rows of the clickable Pods/Services lists to render. This is a dashboard, not a
// resource browser: a hard cap keeps a large cluster from turning this page into an unpaginated
// table, and the tile above already tells the user the true count.
const LIST_ROW_LIMIT = 50;

// `limit` sent on every list request. Without it the API server returns every object of the
// kind, so counting Pods on a large cluster would pull tens of MB of Pod specs through the proxy
// on every load of what is the landing page — and the truncation handling below would be dead
// code, since an unlimited list is never truncated. The count stays exact as long as the server
// reports metadata.remainingItemCount; otherwise the tile shows "N+". Kept comfortably above
// LIST_ROW_LIMIT so the Pods/Services lists are still full pages.
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
];

const TILES: Tile[] = [
    { label: 'Namespaces', resourceKeys: ['namespaces'] },
    { label: 'Pods', resourceKeys: ['pods'] },
    { label: 'Services', resourceKeys: ['services'] },
    { label: 'Deployments', resourceKeys: ['deployments'] },
    { label: 'StatefulSets', resourceKeys: ['statefulsets'] },
    { label: 'DaemonSets', resourceKeys: ['daemonsets'] },
    { label: 'K8s Network Policies', resourceKeys: ['k8sNetworkPolicies'] },
    { label: 'Antrea Network Policies', resourceKeys: ['antreaClusterNetworkPolicies', 'antreaNetworkPolicies'] },
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
        /* Keeps the page-layout spacing between the two list cards, which are wrapped in one
           element so a single class can dim them together. */
        .list-stack {
            display: flex;
            flex-direction: column;
            gap: 1.5rem;
        }
        .clickable-row { cursor: pointer; }
        .clickable-row:hover { background: var(--antrea-color-bg-hover, #2e3f4d); }
    `];

    @state() private _namespace = '';
    @state() private _namespaces: string[] = [];
    @state() private _loading = true;
    // Set while a re-filter is in flight. Unlike _loading it does not blank the page; the tiles
    // stay visible (dimmed) so the namespace <select> survives the update.
    @state() private _refreshing = false;
    @state() private _counts: Record<string, number> = {};
    @state() private _truncated: Record<string, boolean> = {};
    @state() private _pods: NamedItem[] = [];
    @state() private _services: NamedItem[] = [];
    // See antrea-summary-page.ts for why these two are tracked (and reported) separately: one
    // means "the UI is hiding something the user isn't allowed to see", the other means "a call
    // the user IS allowed to make failed for some other reason".
    @state() private _someCardsForbidden = false;
    @state() private _cardErrors: string[] = [];

    private _accessSummary: AccessSummary | null = null;
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
        await this._loadCounts();
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
        const summary = this._accessSummary;

        const results = await Promise.allSettled(RESOURCES.map(r =>
            can(summary, r.gate)
                ? apiFetchJSON<K8sList>(withPageLimit(r.path(this._namespace)))
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
        // Keeps the previous namespace list (and thus the dropdown) intact for a user who can't
        // list namespaces themselves but can read other resources scoped by one.
        let namespaces = this._namespaces;
        let pods: NamedItem[] = [];
        let services: NamedItem[] = [];

        RESOURCES.forEach((r, i) => {
            if (!can(summary, r.gate)) {
                this._someCardsForbidden = true;
                return;
            }
            const result = results[i];
            if (result.status === 'fulfilled') {
                // A page plus the count of what was left off it is the true total. Only when the
                // server truncated without reporting that remainder is the count a floor.
                const remaining = result.value.metadata?.remainingItemCount;
                counts[r.key] = result.value.items.length + (remaining ?? 0);
                truncated[r.key] = Boolean(result.value.metadata?.continue) && remaining === undefined;
                if (r.key === 'namespaces') {
                    namespaces = result.value.items.map(it => it.metadata.name).sort();
                } else if (r.key === 'pods') {
                    pods = result.value.items.map(it => ({ name: it.metadata.name, namespace: it.metadata.namespace ?? '' }));
                } else if (r.key === 'services') {
                    services = result.value.items.map(it => ({ name: it.metadata.name, namespace: it.metadata.namespace ?? '' }));
                }
            } else {
                this._recordFailure(r.label, result.reason);
            }
        });

        this._counts = counts;
        this._truncated = truncated;
        this._namespaces = namespaces;
        this._pods = pods;
        this._services = services;
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

    /** Tells the host to route to the Flow Visibility Service Map with a filter preset — the
     * host owns routing (React Router), this component does not, so it hands off via an event
     * the same way antrea-session-expired does. Array values are comma-joined to match the query
     * param convention the Flow Visibility page's own filters already use. */
    private _navigateToServiceMap(filter: { namespaces?: string[]; podNames?: string[]; serviceNames?: string[] }) {
        const search: Record<string, string> = { view: 'map' };
        if (filter.namespaces?.length) search.namespaces = filter.namespaces.join(',');
        if (filter.podNames?.length) search.pods = filter.podNames.join(',');
        if (filter.serviceNames?.length) search.services = filter.serviceNames.join(',');
        this.dispatchEvent(new CustomEvent('antrea-navigate', { detail: { path: '/flows', search }, bubbles: true, composed: true }));
    }

    private _renderTile(tile: Tile) {
        const present = tile.resourceKeys.filter(k => k in this._counts);
        if (present.length === 0) return '';
        const count = present.reduce((sum, k) => sum + this._counts[k], 0);
        const isTruncated = present.some(k => this._truncated[k]);
        return html`
            <antrea-card heading=${tile.label}>
                <div class="stat-value">${count}${isTruncated ? '+' : ''}</div>
            </antrea-card>
        `;
    }

    private _renderClickableList(heading: string, resourceKey: string, items: NamedItem[], onClick: (item: NamedItem) => void) {
        if (items.length === 0) return '';
        // items is one page of at most PAGE_LIMIT, so its length is not the total — report the
        // tile's count instead, with the same "+" the tile uses when even that is a floor.
        const total = `${this._counts[resourceKey] ?? items.length}${this._truncated[resourceKey] ? '+' : ''}`;
        return html`
            <antrea-card heading=${heading}>
                <table class="data-table" part="table">
                    <thead><tr><th part="table-header-cell">Namespace</th><th part="table-header-cell">Name</th></tr></thead>
                    <tbody>
                        ${items.slice(0, LIST_ROW_LIMIT).map(item => html`
                            <tr class="clickable-row" @click=${() => onClick(item)}>
                                <td part="table-cell">${item.namespace}</td>
                                <td part="table-cell">${item.name}</td>
                            </tr>
                        `)}
                    </tbody>
                </table>
                ${items.length > LIST_ROW_LIMIT ? html`<div class="text-muted">Showing first ${LIST_ROW_LIMIT} of ${total}</div>` : ''}
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
                    <p class="page-title">Overview</p>
                    ${this._someCardsForbidden ? html`
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
                                <option value="" ?selected=${this._namespace === ''}>All Namespaces</option>
                                ${this._namespaces.map(ns => html`<option value=${ns} ?selected=${ns === this._namespace}>${ns}</option>`)}
                            </select>
                        </div>
                    ` : ''}

                    <div class="stat-grid ${this._refreshing ? 'stale' : ''}">
                        ${TILES.map(tile => this._renderTile(tile))}
                    </div>

                    <div class="list-stack ${this._refreshing ? 'stale' : ''}">
                        ${this._renderClickableList('Pods', 'pods', this._pods, p => this._navigateToServiceMap({ namespaces: [p.namespace], podNames: [p.name] }))}
                        ${this._renderClickableList('Services', 'services', this._services, s => this._navigateToServiceMap({ namespaces: [s.namespace], serviceNames: [`${s.namespace}/${s.name}`] }))}
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
