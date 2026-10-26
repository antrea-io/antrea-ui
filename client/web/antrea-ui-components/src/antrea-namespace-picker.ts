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
import { mayNameNamespace, searchNamespaces } from './lib/namespace-access.js';
import type { NamespaceAccess } from './lib/namespace-access.js';
import { SCOPE_ALL, ALL_NAMESPACES_LABEL } from './lib/namespace-scope.js';

/** The event the picker dispatches when the user chooses a scope. */
export const SCOPE_SELECT_EVENT = 'antrea-scope-select';
/** Detail of SCOPE_SELECT_EVENT. */
export interface ScopeSelectDetail {
    /** SCOPE_ALL, or a Namespace name as chosen or typed (not checked: the page rejects a name
     * that is not a valid Namespace name). */
    value: string;
}

interface ScopeItem { value: string; label: string }

/**
 * The picker of a Namespace-aware page (docs/design/namespace-scope-ux.md, rules 2, 7 and 8): a
 * combobox that offers the Namespace list and searches it by prefix, plus a box to name a
 * Namespace when the list is empty, unknown or cut. It checks nothing about a Namespace: the page
 * owns the scope, and is told what the user chose with SCOPE_SELECT_EVENT.
 *
 * It renders into its own light DOM, so that the styles of the page that contains it (see
 * pickerStyles) apply and the page can find its inputs.
 */
export class AntreaNamespacePicker extends LitElement {
    protected override createRenderRoot() { return this; }

    /** What the picker offers. */
    @property({ attribute: false }) access: NamespaceAccess | null = null;
    /** The selected scope: SCOPE_ALL, a Namespace name, or '' for none. It always appears in the
     * picker as selected, even one the list would not offer. */
    @property() scope = '';
    /** Whether All Namespaces is offered: unless the page's verdict is denied. It is always
     * shown while it is the scope in force. */
    @property({ type: Boolean }) offerAll = true;
    @property() label = 'Namespace';
    /** Prefix of the ids of the picker's elements. */
    @property() inputId = 'namespace-picker';
    /** A note on the selected Namespace, shown under the input. */
    @property() note = '';

    @state() private _query = '';
    @state() private _open = false;
    // Which item of the open menu the keyboard is on, or -1 for none.
    @state() private _active = -1;

    private _onDocClick = (e: PointerEvent) => {
        const path = e.composedPath();
        if (!path.some(el => el instanceof Element && el.closest?.('.combobox') && this.contains(el))) {
            this._open = false;
        }
    };

    override connectedCallback() {
        super.connectedCallback();
        window.addEventListener('pointerdown', this._onDocClick);
    }

    override disconnectedCallback() {
        super.disconnectedCallback();
        window.removeEventListener('pointerdown', this._onDocClick);
    }

    override willUpdate(changed: Map<string, unknown>) {
        // The items the highlighted row indexed were the ones before the list changed.
        if (changed.has('access')) this._active = -1;
    }

    /** The items offered. The scope in force always appears among them, even one the list would
     * not offer, but only while nothing is typed: a search only filters the list, so what is
     * typed never offers a name the list lacks, and All Namespaces is hidden while typing. */
    private get _items(): ScopeItem[] {
        const typed = this._query.trim();
        const items: ScopeItem[] = [];
        if (!typed && (this.offerAll || this.scope === SCOPE_ALL)) {
            items.push({ value: SCOPE_ALL, label: ALL_NAMESPACES_LABEL });
        }
        const ns = new Set(this._search.items);
        if (!typed && this.scope && this.scope !== SCOPE_ALL) ns.add(this.scope);
        for (const n of Array.from(ns).sort()) items.push({ value: n, label: n });
        return items;
    }

    /** The namespaces matching what is typed, derived rather than stored so that they cannot
     * drift from the query or from the list. */
    private get _search(): { items: string[], more: boolean } {
        return searchNamespaces(this.access, this._query);
    }

    private _openMenu() {
        if (this._open) return;
        this._open = true;
        this._active = -1;
        // A menu that was last left narrowed starts from the whole list again.
        this._query = '';
    }

    /** Closes the menu and drops what was typed, for a page that opens another menu. */
    close() {
        this._open = false;
        this._query = '';
        this._active = -1;
    }

    private _choose(value: string) {
        this.close();
        this._emit(value);
    }

    private _emit(value: string) {
        this.dispatchEvent(new CustomEvent<ScopeSelectDetail>(SCOPE_SELECT_EVENT, {
            detail: { value },
            bubbles: true,
            composed: true,
        }));
    }

    private _onKeydown(e: KeyboardEvent) {
        const items = this._items;
        switch (e.key) {
        case 'ArrowDown':
        case 'ArrowUp': {
            e.preventDefault();
            this._openMenu();
            if (items.length === 0) return;
            const step = e.key === 'ArrowDown' ? 1 : -1;
            // With nothing highlighted, ArrowDown starts at the first option and ArrowUp at the last.
            this._active = this._active < 0
                ? (step > 0 ? 0 : items.length - 1)
                : (this._active + step + items.length) % items.length;
            break;
        }
        case 'Enter': {
            e.preventDefault();
            const typed = this._query.trim();
            // The highlighted row only counts while the menu is open: once closed it indexes a list
            // the user is no longer looking at.
            if (this._open && this._active >= 0 && this._active < items.length) this._choose(items[this._active].value);
            // A name typed in full is the one meant, even when it also starts another Namespace.
            else if (typed && items.some(i => i.value === typed.toLowerCase())) this._choose(typed.toLowerCase());
            // A single match is the one meant; and where the list is empty, unknown or cut there
            // may be nothing to match against, so the typed name is the scope (the API decides).
            else if (typed && items.length === 1) this._choose(items[0].value);
            else if (typed && mayNameNamespace(this.access)) this._choose(typed);
            break;
        }
        case 'Escape':
            this.close();
            break;
        case 'Tab':
            this._open = false;
            break;
        }
    }

    override render() {
        const value = this.scope;
        const label = value === SCOPE_ALL ? ALL_NAMESPACES_LABEL : value;
        const items = this._items;
        const open = this._open;
        const id = this.inputId;
        return html`
            <div class="field-group scope-select combobox" style="min-width:220px">
                <label class="field-label" for=${id}>${this.label}</label>
                <input id=${id} class="field-input" type="text" role="combobox" autocomplete="off"
                    placeholder="Select a namespace…"
                    aria-expanded=${open ? 'true' : 'false'} aria-controls="${id}-list"
                    aria-activedescendant=${open && this._active >= 0 ? `${id}-opt-${this._active}` : nothing}
                    data-value=${value}
                    .value=${open ? this._query : label}
                    @focus=${() => this._openMenu()}
                    @click=${() => this._openMenu()}
                    @input=${(e: Event) => { this._openMenu(); this._query = (e.target as HTMLInputElement).value; this._active = -1; }}
                    @keydown=${(e: KeyboardEvent) => this._onKeydown(e)}>
                ${open ? html`
                    <ul id="${id}-list" class="combobox-list" role="listbox">
                        ${items.length === 0 ? html`<li class="combobox-empty">No matching namespaces</li>` : nothing}
                        ${items.map((item, i) => html`
                            <li id="${id}-opt-${i}" role="option" data-value=${item.value}
                                aria-selected=${item.value === value ? 'true' : 'false'}
                                class="combobox-option ${item.value === value ? 'selected' : ''} ${i === this._active ? 'active' : ''}"
                                @mousedown=${(e: Event) => e.preventDefault()}
                                @click=${() => this._choose(item.value)}>${item.label}</li>
                        `)}
                        ${this._search.more ? html`<li class="combobox-empty">${this._query.trim() ? 'More matches, keep typing' : 'Showing the first 10 - type to search'}</li>` : nothing}
                    </ul>
                ` : nothing}
                ${this.note ? html`<p class="text-muted" data-testid="scope-note">${this.note}</p>` : nothing}
            </div>
            ${mayNameNamespace(this.access) ? this._renderByName() : nothing}
        `;
    }

    /** A free-text way to name a Namespace the list does not offer. Only rendered when the list is
     * empty, unknown or cut (rule 8): when it is complete and not empty, a name absent from it is
     * one the user most likely cannot use, so a text box could only invite a refused request. Only
     * the name's form is checked, by the page, as for any typed name. */
    private _renderByName() {
        const submit = (e: Event) => {
            const input = e.target as HTMLInputElement;
            // Not trimmed: a name that is not a valid Namespace name is rejected as typed.
            const ns = input.value;
            if (!ns) return;
            input.value = '';
            this._emit(ns);
        };
        return html`
            <div class="field-group" style="min-width:200px">
                <label class="field-label" for="${this.inputId}-by-name">Namespace not listed</label>
                <input id="${this.inputId}-by-name" class="field-input" type="text" placeholder="Type a name, then Enter"
                    @keydown=${(e: KeyboardEvent) => { if (e.key === 'Enter') submit(e); }}>
            </div>
        `;
    }
}

customElements.define('antrea-namespace-picker', AntreaNamespacePicker);

declare global {
    interface HTMLElementTagNameMap { 'antrea-namespace-picker': AntreaNamespacePicker; }
}
