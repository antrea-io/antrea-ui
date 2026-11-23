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

import { LitElement, html, nothing } from 'lit';
import { property, state } from 'lit/decorators.js';
import { namespaceScope, namespaceScopeSupported, registerRoute, registerSidebarEntry } from '@antrea/ui-plugin-sdk';
import type { NamespaceScope, NamespaceScopeState } from '@antrea/ui-plugin-sdk';

const PODS = [{ group: '', resource: 'pods', verb: 'list' }];

// The host owns the Namespace picker and its rules. This plugin owns its requests: it reloads
// only when the effective scope changes, cancels obsolete work, and handles the API's refusal.
class AntreaPluginPodCounter extends LitElement {
    @property({ attribute: false }) locationSearch?: string;
    @state() private _scope: NamespaceScope | null = null;
    @state() private _count: number | null = null;
    @state() private _error: string | null = null;
    private _requestScope?: string;
    private _request?: AbortController;

    override disconnectedCallback() {
        super.disconnectedCallback();
        this._request?.abort();
        this._requestScope = undefined;
    }

    private _onScopeState(event: CustomEvent<NamespaceScopeState>) {
        if (!this.isConnected) return;
        const scope = event.detail.scope;
        this._scope = scope;
        // Access and notes also produce state events; neither should repeat the data request.
        const key = namespaceScope().search(scope);
        if (key === this._requestScope) return;
        this._requestScope = key;
        void this._load(scope);
    }

    private async _load(scope: NamespaceScope | null) {
        this._request?.abort();
        this._count = null;
        this._error = null;
        if (!scope) return;
        const request = new AbortController();
        this._request = request;
        const path = scope.kind === 'all' ? 'pods' : `namespaces/${encodeURIComponent(scope.namespace)}/pods`;
        try {
            const response = await fetch(`/api/v1/k8s/api/v1/${path}`, {
                credentials: 'include', signal: request.signal,
            });
            if (request.signal.aborted) return;
            if (response.status === 401) {
                this.dispatchEvent(new CustomEvent('antrea-session-expired', { bubbles: true, composed: true }));
                return;
            }
            if (response.status === 403) throw new Error('You do not have permission to list Pods in this scope.');
            if (!response.ok) throw new Error(`Request failed: ${response.status}`);
            const data = await response.json();
            if (!request.signal.aborted) this._count = data.items?.length ?? 0;
        } catch (error) {
            if (!request.signal.aborted) this._error = error instanceof Error ? error.message : String(error);
        }
    }

    override render() {
        if (!namespaceScopeSupported()) return html`<p>This page needs a newer Antrea UI.</p>`;
        const label = this._scope?.kind === 'all' ? 'cluster' : this._scope?.namespace;
        return html`
            <h1>Pod Counter</h1>
            <antrea-namespace-scope .locationSearch=${this.locationSearch} .resources=${PODS}
                @antrea-namespace-scope-state=${this._onScopeState}></antrea-namespace-scope>
            ${this._error ? html`<p role="alert">${this._error}</p>
                <button @click=${() => this._load(this._scope)}>Retry</button>` : nothing}
            ${!this._scope ? html`<p>Select a Namespace to count its Pods.</p>`
                : !this._error ? html`<p>Pods in ${label}: ${this._count ?? '...'}</p>` : nothing}
        `;
    }
}

customElements.define('antrea-plugin-pod-counter', AntreaPluginPodCounter);

registerRoute({ path: '/plugin/pod-counter', tag: 'antrea-plugin-pod-counter',
    requires: { namespaceScope: true, resources: PODS } });
registerSidebarEntry({
    label: 'Pod Counter',
    path: '/plugin/pod-counter',
    icon: 'M7.752.066a.5.5 0 0 1 .496 0l3.75 2.143a.5.5 0 0 1 .252.434v3.995l3.498 2A.5.5 0 0 1 16 9.07v4.286a.5.5 0 0 1-.252.434l-3.75 2.143a.5.5 0 0 1-.496 0l-3.502-2-3.502 2.001a.5.5 0 0 1-.496 0l-3.75-2.143A.5.5 0 0 1 0 13.357V9.071a.5.5 0 0 1 .252-.434L3.75 6.638V2.643a.5.5 0 0 1 .252-.434zM4.25 7.504 1.508 9.071l2.742 1.567 2.742-1.567zM7.5 9.933l-2.75 1.571v3.134l2.75-1.571zm1 3.134 2.75 1.571v-3.134L8.5 9.933zm.508-3.996 2.742 1.567 2.742-1.567-2.742-1.567zm2.242-2.433V3.504L8.5 5.076V8.21zM7.5 8.21V5.076L4.75 3.504v3.134zM5.258 2.643 8 4.21l2.742-1.567L8 1.076zM15 9.933l-2.75 1.571v3.134L15 13.067zM3.75 14.638v-3.134L1 9.933v3.134z',
});
