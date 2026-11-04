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

import { css, html, nothing } from 'lit';
import { state } from 'lit/decorators.js';
import { repeat } from 'lit/directives/repeat.js';
import { APIError, apiFetch, apiFetchJSON } from '../lib/api.js';
import {
    accessSummary, can, GATE_SUPPORTBUNDLE_LIST, GATE_SUPPORTBUNDLE_CREATE,
    GATE_SUPPORTBUNDLE_GET, GATE_SUPPORTBUNDLE_DELETE,
} from '../lib/access-api.js';
import type { AccessSummary, ResourceQuery } from '../lib/access-api.js';
import { formatBytes } from '../lib/flow-types.js';
import { SessionAwarePage } from '../lib/session-aware-page.js';
import { pageStyles } from '../lib/styles.js';
import { supportBundleDownloadURL, supportBundleErrorMessage, supportBundlePath } from '../lib/supportbundle-api.js';
import type { SupportBundle, SupportBundleStatus } from '../lib/supportbundle-api.js';
import '../antrea-alert.js';
import '../antrea-button.js';
import '../antrea-card.js';

const COLLECTION_STARTED_NOTICE = 'Collection started. You can leave this page while it runs.';
// Kept when a new collection starts: the slot and disk space of a deleted Collecting bundle stay
// held while it stops, and this notice is what explains a 429 from that Collect.
const COLLECTION_STOPPING_NOTICE = 'Bundle deleted. Collection is stopping; its concurrency slot and disk space may take up to about 50 seconds to be released.';
const POLL_INTERVAL_MS = 2_000;
const REQUEST_TIMEOUT_MS = 30_000;

export class AntreaSupportBundlePage extends SessionAwarePage {
    static styles = [pageStyles, css`
        .toolbar { display: flex; align-items: center; justify-content: space-between; gap: 1rem; }
        .table-scroll { overflow-x: auto; }
        .data-table td { text-align: left; vertical-align: top; }
        .bundle-id { overflow-wrap: anywhere; font-family: monospace; }
        .actions { display: flex; align-items: center; gap: 0.75rem; }
        a { color: var(--antrea-color-primary, #0079b8); }
        a[aria-disabled="true"] { opacity: 0.45; cursor: not-allowed; }
        .status { font-weight: 600; }
        .status-failed { color: var(--antrea-color-danger, #f54f47); }
        .status-collected { color: var(--antrea-color-success, #60b515); }
        .partial { color: var(--antrea-color-warning, #ffce5a); }
        .error { overflow-wrap: anywhere; white-space: pre-wrap; }
        .size { white-space: nowrap; }
        details { margin-top: 0.5rem; }
        summary { cursor: pointer; }
        details .data-table { margin-top: 0.5rem; }
    `];

    @state() private _summary: AccessSummary | null = null;
    @state() private _permissionsReady = false;
    @state() private _bundles: SupportBundle[] = [];
    @state() private _loaded = false;
    @state() private _busy = '';
    @state() private _error = '';
    @state() private _notice = '';
    @state() private _disabled = false;
    @state() private _sessionEnded = false;
    private _pollFailed = false;
    private _listError = false;
    private _backgroundRequest = false;
    private _generation = 0;
    private _pollTimer?: ReturnType<typeof setTimeout>;
    private _request?: AbortController;

    protected override onSessionReady() {
        const generation = ++this._generation;
        this._abortRequest();
        this._permissionsReady = false;
        this._sessionEnded = false;
        this._busy = '';
        document.addEventListener('visibilitychange', this._onVisibilityChange);
        void this._loadPermissions(generation);
    }

    override disconnectedCallback() {
        ++this._generation;
        this._clearPoll();
        this._abortRequest();
        document.removeEventListener('visibilitychange', this._onVisibilityChange);
        super.disconnectedCallback();
    }

    /** A superseded request's `finally` skips its cleanup (its generation is stale), so whoever supersedes it clears the request state: here, or by overwriting it in `_run`. */
    private _abortRequest() {
        this._request?.abort();
        this._request = undefined;
        this._backgroundRequest = false;
    }

    private async _loadPermissions(generation: number) {
        let summary: AccessSummary | null = null;
        try {
            summary = await accessSummary();
        } catch (error) {
            if (generation !== this._generation) return;
            if (this.isSessionExpiredError(error)) {
                this._sessionEnded = true;
                this.dispatchSessionExpired();
                return;
            }
            // Like the shell, unknown access fails open; the backend still authorizes every call.
        }
        if (generation !== this._generation) return;
        this._summary = summary;
        this._permissionsReady = true;
        this._refresh();
    }

    private _allowed(gate: ResourceQuery): boolean {
        return this._permissionsReady && !this._sessionEnded && can(this._summary, gate);
    }

    private _clearPoll() {
        clearTimeout(this._pollTimer);
        this._pollTimer = undefined;
    }

    private _schedulePoll() {
        this._clearPoll();
        if (!this.isConnected || document.hidden || this._busy || this._disabled || this._pollFailed ||
            !this._allowed(GATE_SUPPORTBUNDLE_LIST) || !this._bundles.some(b => b.status === 'Collecting')) return;
        // Re-list instead of GET /status: list-only users can also follow progress, and bundles
        // deleted by another user or expired disappear without needing a separate GET grant.
        this._pollTimer = setTimeout(() => this._refresh(true), POLL_INTERVAL_MS);
    }

    private _onVisibilityChange = () => {
        this._clearPoll();
        if (!document.hidden) this._refresh(true);
    };

    /**
     * Serialize mutations with re-listing so a stale list cannot undo a successful action. A
     * background re-list is invisible (it never sets _busy) and yields to a user action, so a
     * click is never dropped because a poll happens to be in flight.
     */
    private async _run<T>(
        action: string,
        request: (signal: AbortSignal) => Promise<T>,
        apply: (result: T) => void,
        missingID?: string,
        background = false,
    ) {
        if (this._sessionEnded || !this.isConnected) return;
        if (this._request) {
            if (!this._backgroundRequest || background) return;
            this._request.abort();
            ++this._generation;
        }
        const generation = this._generation;
        const controller = new AbortController();
        this._request = controller;
        this._backgroundRequest = background;
        this._clearPoll();
        this._busy = background ? '' : action;
        if (!background) {
            this._error = '';
            this._listError = false;
        }
        this._pollFailed = false;
        const timeout = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
        try {
            const result = await request(controller.signal);
            if (generation !== this._generation) return;
            apply(result);
            if (action === 'refresh' && this._listError) {
                this._error = '';
                this._listError = false;
            }
        } catch (error) {
            if (generation !== this._generation) return;
            // A rejected action (e.g. a 429) must not stop other bundles' progress.
            this._pollFailed = action === 'refresh';
            this._listError = action === 'refresh';
            if (this.isSessionExpiredError(error)) {
                this._sessionEnded = true;
                this.dispatchSessionExpired();
            } else if (error instanceof APIError && error.code === 404 && missingID) {
                this._bundles = this._bundles.filter(b => b.id !== missingID);
                this._notice = 'This bundle was deleted or has expired. It has been removed from the list.';
                this._pollFailed = false;
            } else {
                this._disabled = error instanceof APIError && error.code === 501;
                this._error = controller.signal.aborted
                    ? 'The request timed out. Refresh the list before retrying an action.'
                    : supportBundleErrorMessage(error);
            }
        } finally {
            clearTimeout(timeout);
            if (generation === this._generation) {
                this._request = undefined;
                this._busy = '';
                this._schedulePoll();
            }
        }
    }

    private _refresh(background = false) {
        if (document.hidden || !this._allowed(GATE_SUPPORTBUNDLE_LIST)) return;
        void this._run('refresh', signal => apiFetchJSON<{ items: SupportBundle[] }>('supportbundle', { signal }),
            result => {
                this._bundles = result.items;
                // The banner is only true while a collection runs; drop it once a re-list,
                // automatic or manual, shows that none does.
                if (this._notice === COLLECTION_STARTED_NOTICE &&
                    !result.items.some(b => b.status === 'Collecting')) {
                    this._notice = '';
                }
                this._loaded = true;
                this._disabled = false;
            }, undefined, background);
    }

    private _collect(event: Event) {
        event.preventDefault();
        if (this._busy || this._disabled || !this._allowed(GATE_SUPPORTBUNDLE_CREATE)) return;
        if (this._notice !== COLLECTION_STOPPING_NOTICE) this._notice = '';
        void this._run('collect', signal => apiFetchJSON<SupportBundle>('supportbundle', {
            method: 'POST', signal,
        }), bundle => {
            this._bundles = [...this._bundles.filter(b => b.id !== bundle.id), bundle];
            this._notice = COLLECTION_STARTED_NOTICE;
        });
    }

    private _delete(bundle: SupportBundle) {
        if (this._busy || this._disabled || !this._allowed(GATE_SUPPORTBUNDLE_DELETE)) return;
        const collecting = bundle.status === 'Collecting';
        const message = `Delete support bundle ${bundle.id} for all users?` +
            (collecting ? ' This also cancels its collection. Resources may take up to about 50 seconds to be released.' : '');
        if (!window.confirm(message)) return;
        this._notice = '';
        void this._run('delete', signal => apiFetch(supportBundlePath(bundle.id), { method: 'DELETE', signal }), () => {
            this._bundles = this._bundles.filter(b => b.id !== bundle.id);
            this._notice = collecting
                ? COLLECTION_STOPPING_NOTICE
                : 'Bundle deleted for all users.';
        }, bundle.id);
    }

    private _download(event: Event, bundle: SupportBundle) {
        event.preventDefault();
        if (this._busy || this._disabled || bundle.status !== 'Collected' || !this._allowed(GATE_SUPPORTBUNDLE_GET)) return;
        this._notice = '';
        void this._run('download', signal => apiFetchJSON<SupportBundle>(`${supportBundlePath(bundle.id)}/status`, { signal }),
            current => {
                this._bundles = this._bundles.map(b => b.id === bundle.id ? current : b);
                if (current.status !== 'Collected') {
                    this._notice = 'This bundle is no longer ready to download.';
                    return;
                }
                // Only metadata is fetched. Let the browser stream the archive to disk, with the
                // session cookie and the filename supplied by Content-Disposition. In dev this
                // must be a top-level navigation, not an iframe or a fetch of the tarball.
                const link = document.createElement('a');
                link.href = supportBundleDownloadURL(bundle.id);
                link.download = '';
                document.body.appendChild(link);
                link.click();
                link.remove();
            }, bundle.id);
    }

    private _status(status: SupportBundleStatus) {
        return html`<span class="status status-${status.toLowerCase()}">${status}</span>`;
    }

    private _size(size?: number) {
        return size === undefined ? '—' : html`<span class="size">${formatBytes(size)}</span>`;
    }

    private _date(value: string) {
        return html`<time datetime=${value} title=${value}>${new Date(value).toLocaleString()}</time>`;
    }

    private _details(bundle: SupportBundle) {
        return html`
            ${bundle.error ? html`<p class="error" role="alert">${bundle.error}</p>` : nothing}
            ${bundle.sources?.length ? html`
                <details>
                    <summary>Sources (${bundle.sources.length})</summary>
                    <table class="data-table" aria-label="Sources for ${bundle.id}">
                        <thead><tr><th scope="col">Name</th><th scope="col">Kind</th><th scope="col">Status</th>
                            <th scope="col">Size</th><th scope="col">Error</th></tr></thead>
                        <tbody>${bundle.sources.map(source => html`
                            <tr><td>${source.name}</td><td>${source.kind}</td><td>${this._status(source.status)}</td>
                                <td>${this._size(source.size)}</td><td class="error">${source.error ?? '—'}</td></tr>
                        `)}</tbody>
                    </table>
                </details>
            ` : nothing}
        `;
    }

    override render() {
        const unavailable = !!this._busy || this._disabled || this._sessionEnded;
        return html`
            <main><div class="page-layout">
                <h1 class="page-title">Support Bundle</h1>
                <antrea-alert status="info">
                    Bundles contain admin-level diagnostics collected as the antrea-ui-admin ServiceAccount.
                    They are shared: users with the corresponding permissions can list, download and delete any bundle.
                    Bundles expire after 6 hours by default and are lost when the Pod is replaced.
                </antrea-alert>
                ${this._error ? html`<antrea-alert status="danger" role="alert">${this._error}</antrea-alert>` : nothing}
                ${this._notice ? html`<antrea-alert status="info" role="status">${this._notice}</antrea-alert>` : nothing}
                ${!this._permissionsReady ? html`<p role="status">Loading permissions…</p>` :
                    !this._allowed(GATE_SUPPORTBUNDLE_LIST) ? html`
                        <antrea-alert status="warning">You do not have permission to view this page.</antrea-alert>
                    ` : html`
                        ${this._allowed(GATE_SUPPORTBUNDLE_CREATE) ? html`
                            <antrea-card heading="Collect a bundle">
                                <form class="form-stack" @submit=${this._collect}>
                                    <div><antrea-button id="collect" type="submit" ?disabled=${unavailable}>
                                        ${this._busy === 'collect' ? 'Starting…' : 'Collect'}
                                    </antrea-button></div>
                                </form>
                            </antrea-card>
                        ` : nothing}
                        <div class="toolbar">
                            <h2 class="page-title">Bundles</h2>
                            <antrea-button id="refresh" action="outline" ?disabled=${!!this._busy || this._sessionEnded}
                                @click=${() => this._refresh()}>${this._busy === 'refresh' ? 'Refreshing…' : 'Refresh'}</antrea-button>
                        </div>
                        ${!this._loaded && this._busy === 'refresh' ? html`<p role="status">Loading bundles…</p>` : nothing}
                        ${this._loaded && !this._bundles.length ? html`<p>No support bundles are available.</p>` : nothing}
                        ${this._bundles.length ? html`
                            <div class="table-scroll"><table class="data-table" aria-label="Support bundles">
                                <thead><tr><th scope="col">ID / Details</th><th scope="col">Status</th>
                                    <th scope="col">Size</th><th scope="col">Created</th><th scope="col">Created by</th>
                                    <th scope="col">Expires</th><th scope="col">Actions</th></tr></thead>
                                <tbody>${repeat(this._bundles, b => b.id, bundle => html`
                                    <tr data-bundle-id=${bundle.id}>
                                        <td><span class="bundle-id">${bundle.id}</span>${this._details(bundle)}</td>
                                        <td>${this._status(bundle.status)}
                                            ${bundle.status === 'Collected' && bundle.sources?.some(s => s.status === 'Failed')
                                                ? html`<div class="partial">Partial bundle: some sources failed.</div>` : nothing}
                                        </td>
                                        <td>${this._size(bundle.size)}</td><td>${this._date(bundle.createdAt)}</td>
                                        <td>${bundle.createdBy ?? '—'}</td><td>${this._date(bundle.expiresAt)}</td>
                                        <td><div class="actions">
                                            ${bundle.status === 'Collected' && this._allowed(GATE_SUPPORTBUNDLE_GET) ? html`
                                                <a href=${supportBundleDownloadURL(bundle.id)} download
                                                    aria-label="Download ${bundle.id}" aria-disabled=${unavailable}
                                                    @click=${(event: Event) => this._download(event, bundle)}>Download</a>
                                            ` : nothing}
                                            ${this._allowed(GATE_SUPPORTBUNDLE_DELETE) ? html`
                                                <antrea-button action="outline" ?disabled=${unavailable}
                                                    @click=${() => this._delete(bundle)}>Delete</antrea-button>
                                            ` : nothing}
                                        </div></td>
                                    </tr>
                                `)}</tbody>
                            </table></div>
                        ` : nothing}
                    `}
            </div></main>
        `;
    }
}

customElements.define('antrea-support-bundle-page', AntreaSupportBundlePage);

declare global {
    interface HTMLElementTagNameMap { 'antrea-support-bundle-page': AntreaSupportBundlePage; }
}
