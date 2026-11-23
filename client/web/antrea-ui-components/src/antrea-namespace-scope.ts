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

import { LitElement, css, html, nothing } from 'lit';
import { property, state } from 'lit/decorators.js';
import type { NamespaceScopeHandle, NamespaceScopeResources, NamespaceScopeState } from './lib/namespace-scope-controller.js';
import { createPluginNamespaceScope } from './lib/plugin-namespace-scope.js';
import type { PluginNamespaceScope } from './lib/plugin-namespace-scope.js';
import { scopeToValue, valueToScope } from './lib/namespace-scope.js';
import { pageStyles, pickerStyles } from './lib/styles.js';
import type { ScopeSelectDetail } from './antrea-namespace-picker.js';
import './antrea-namespace-picker.js';
import './antrea-alert.js';

/** The event <antrea-namespace-scope> dispatches whenever its state changes. It bubbles but is
 * not composed: it is for the plugin that contains the element, not for the host. */
export const NAMESPACE_SCOPE_STATE_EVENT = 'antrea-namespace-scope-state';

/**
 * The Namespace picker of a Namespace-aware plugin page, with the page-side scope rules behind it:
 * follows the URL, settles on a default, validates typed names, and tells the host what the user
 * chose. A plugin renders it and listens for
 * NAMESPACE_SCOPE_STATE_EVENT. It renders into its own shadow root, so the plugin's styles
 * neither break it nor are needed by it.
 *
 * Give it the page's `locationSearch` and the same `resources` as the route's `requires`.
 */
export class AntreaNamespaceScope extends LitElement {
    static override styles = [pageStyles, pickerStyles, css`
        :host { display: block; }
    `];

    /** The `locationSearch` the host set on the plugin element. Absent: window.location.search. */
    @property({ attribute: false }) locationSearch?: string;
    /** The page's permissions, any-of, as in `requires.resources`. */
    @property({ attribute: false }) resources?: NamespaceScopeResources;
    @property() label = 'Namespace';
    @property() inputId = 'namespace-scope-picker';

    @state() private _state: NamespaceScopeState | null = null;
    private _handle?: NamespaceScopeHandle;
    private _unsubscribe?: () => void;

    /** The current state, or null before the element has connected. */
    get scope(): NamespaceScopeState | null {
        return this._state;
    }

    override connectedCallback() {
        super.connectedCallback();
        // The host's own factory when a plugin host provides one (it knows its router), else the
        // events a host listens for on the element.
        const host = (window as { __antreaPluginHost?: { namespaceScope?: PluginNamespaceScope } }).__antreaPluginHost;
        const factory = host?.namespaceScope ?? createPluginNamespaceScope();
        this._handle = factory.create({ target: this, resources: this.resources });
        this._unsubscribe = this._handle.subscribe(s => this._onState(s));
        this._handle.setLocationSearch(this.locationSearch);
    }

    override disconnectedCallback() {
        super.disconnectedCallback();
        this._unsubscribe?.();
        this._handle?.dispose();
        this._handle = undefined;
        this._state = null;
    }

    override willUpdate(changed: Map<string, unknown>) {
        super.willUpdate(changed);
        if (!this._handle) return;
        if (changed.has('locationSearch')) {
            this._handle.setLocationSearch(this.locationSearch, { resources: this.resources });
        } else if (changed.has('resources')) {
            this._handle.setResources(this.resources);
        }
    }

    private _onState(s: NamespaceScopeState) {
        this._state = s;
        this.dispatchEvent(new CustomEvent<NamespaceScopeState>(NAMESPACE_SCOPE_STATE_EVENT, {
            detail: s, bubbles: true,
        }));
    }

    private _onSelect(e: CustomEvent<ScopeSelectDetail>) {
        const scope = valueToScope(e.detail.value);
        if (scope) this._handle?.select(scope, e.detail.typed);
    }

    override render() {
        const s = this._state;
        return html`
            ${s?.error ? html`<antrea-alert status="danger">${s.error}</antrea-alert>` : nothing}
            <antrea-namespace-picker
                .label=${this.label} .inputId=${this.inputId}
                .access=${s?.access ?? null}
                .scope=${scopeToValue(s?.scope ?? null)}
                .offerAll=${s?.offerAll ?? true}
                .note=${s?.note?.text ?? ''}
                @antrea-scope-select=${(e: CustomEvent<ScopeSelectDetail>) => this._onSelect(e)}>
            </antrea-namespace-picker>
        `;
    }
}

customElements.define('antrea-namespace-scope', AntreaNamespaceScope);

declare global {
    interface HTMLElementTagNameMap { 'antrea-namespace-scope': AntreaNamespaceScope }
}
