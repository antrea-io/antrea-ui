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

import { LitElement, html, css } from 'lit';
import type { PropertyValues } from 'lit';
import { property } from 'lit/decorators.js';

/**
 * Sidebar navigation container.
 * Collapses to icon-only mode when `expanded` is false.
 *
 * @slot - antrea-nav-item elements
 * @fires antrea-toggle - When the collapse/expand toggle is clicked
 *
 * CSS tokens consumed:
 *   --antrea-nav-width-expanded, --antrea-nav-width-collapsed,
 *   --antrea-nav-bg, --antrea-nav-border,
 *   --antrea-color-text-muted, --antrea-font-size-sm,
 *   --antrea-space-xs, --antrea-space-sm
 */
export class AntreaNav extends LitElement {
    static styles = css`
        :host {
            display: block;
            flex-shrink: 0;
        }

        nav {
            display: flex;
            flex-direction: column;
            height: 100%;
            min-height: 100vh;
            width: var(--antrea-nav-width-expanded, 200px);
            background: var(--antrea-nav-bg, #17242b);
            border-right: 1px solid var(--antrea-nav-border, #243340);
            overflow: hidden;
            transition: width 0.2s ease;
        }

        :host(:not([expanded])) nav {
            width: var(--antrea-nav-width-collapsed, 48px);
        }

        .nav-toggle {
            display: flex;
            align-items: center;
            gap: var(--antrea-space-sm, 0.5rem);
            padding: var(--antrea-space-sm, 0.5rem) var(--antrea-space-sm, 0.5rem);
            background: none;
            border: none;
            border-bottom: 1px solid var(--antrea-nav-border, #243340);
            color: var(--antrea-color-text-muted, #adbbc4);
            cursor: pointer;
            font-family: var(--antrea-font-family, sans-serif);
            font-size: var(--antrea-font-size-sm, 0.75rem);
            width: 100%;
            text-align: left;
            white-space: nowrap;
            overflow: hidden;
        }

        .nav-toggle:hover {
            color: var(--antrea-nav-item-text-active, #e9ecef);
            background: var(--antrea-nav-item-bg-hover, #1e2f3a);
        }

        .toggle-icon {
            flex-shrink: 0;
            font-size: 1rem;
            width: 24px;
            text-align: center;
            transition: transform 0.2s;
        }

        :host(:not([expanded])) .toggle-icon {
            transform: rotate(180deg);
        }

        .toggle-label {
            overflow: hidden;
            opacity: 1;
            transition: opacity 0.15s;
        }

        :host(:not([expanded])) .toggle-label {
            opacity: 0;
            width: 0;
        }

        .nav-items {
            flex: 1;
            overflow-y: auto;
            overflow-x: hidden;
        }
    `;

    @property({ type: Boolean, reflect: true }) expanded = true;

    private _handleToggle() {
        this.expanded = !this.expanded;
        this.dispatchEvent(new CustomEvent('antrea-toggle', {
            detail: { expanded: this.expanded },
            bubbles: true,
            composed: true,
        }));
    }

    render() {
        return html`
            <nav aria-label="Main navigation">
                <button class="nav-toggle" @click=${this._handleToggle} aria-label="Toggle navigation">
                    <span class="toggle-icon">☰</span>
                    <span class="toggle-label">Menu</span>
                </button>
                <div class="nav-items">
                    <slot></slot>
                </div>
            </nav>
        `;
    }
}

/**
 * A single item inside antrea-nav.
 * Wrap your router link or anchor inside this element.
 *
 * @slot - Link element (e.g. <a href="...">Label</a>)
 * @attr active - Set to highlight this item as the current page
 *
 * CSS tokens consumed:
 *   --antrea-nav-item-text, --antrea-nav-item-text-active,
 *   --antrea-nav-item-bg-active, --antrea-nav-item-bg-hover,
 *   --antrea-font-size-base, --antrea-space-sm
 */
export class AntreaNavItem extends LitElement {
    static styles = css`
        :host {
            display: block;
        }

        .nav-item {
            display: flex;
            align-items: center;
            overflow: hidden;
        }

        ::slotted(a) {
            display: flex;
            align-items: center;
            gap: var(--antrea-space-sm, 0.5rem);
            padding: var(--antrea-space-sm, 0.5rem) var(--antrea-space-sm, 0.5rem);
            width: 100%;
            color: var(--antrea-nav-item-text, #adbbc4);
            text-decoration: none;
            font-family: var(--antrea-font-family, sans-serif);
            font-size: var(--antrea-font-size-base, 0.875rem);
            white-space: nowrap;
            overflow: hidden;
            transition: background 0.15s, color 0.15s;
        }

        ::slotted(a:hover) {
            background: var(--antrea-nav-item-bg-hover, #1e2f3a);
            color: var(--antrea-nav-item-text-active, #e9ecef);
        }

        :host([active]) ::slotted(a) {
            background: var(--antrea-nav-item-bg-active, #243340);
            color: var(--antrea-nav-item-text-active, #e9ecef);
            font-weight: 500;
            border-left: 3px solid var(--antrea-color-primary, #0079b8);
        }
    `;

    @property({ type: Boolean, reflect: true }) active = false;

    render() {
        return html`
            <div class="nav-item">
                <slot></slot>
            </div>
        `;
    }
}

/**
 * Collapsible wrapper for a nested group of antrea-nav-item elements — e.g. Flow Visibility's
 * Flow List / Service Map sub-pages, or a plugin's own nested entries (see
 * `@antrea/ui-plugin-sdk`'s `PluginSidebarEntry.parentPath`). Active-item highlighting is not
 * this component's job: put an `active` antrea-nav-item in the "header" slot for the group's own
 * page, and give nested antrea-nav-items their own `active` as usual — this component only owns
 * the expand/collapse chrome, so any host or plugin gets the same nested-nav rendering without
 * reimplementing it.
 *
 * Clicking the header slot (e.g. its Link) always expands — never collapses — since it also
 * navigates to the group's own page, and hiding the destination just navigated to would be
 * confusing. Only the chevron button actually toggles, including collapsing when expanded.
 *
 * @slot header - The group's own antrea-nav-item (its link, icon, active state)
 * @slot - Nested antrea-nav-item elements, shown when expanded
 * @attr expanded - Reflects whether the group is currently expanded
 * @prop hasActiveChild - Set by the host when one of the nested items is the current page, so the
 *   group expands instead of hiding the active page behind a collapsed toggle. Consulted on
 *   every false-to-true transition, not just the first — a host route guard (e.g. a redirect
 *   still in flight, or the access summary not having loaded yet, see nav.tsx) can render this
 *   group before it knows the current page belongs inside it — but a true-to-false transition
 *   never auto-collapses, so the user's own toggle still sticks.
 *
 * @csspart toggle - The expand/collapse chevron button. Exposed so a host can hide it in contexts
 *   where there's no room for a group's nested items regardless of expand state (see App.css's
 *   icon-only-sidebar rule, which hides both) — treat this as a stable, external contract, not an
 *   implementation detail.
 *
 * CSS tokens consumed:
 *   --antrea-nav-item-text, --antrea-nav-item-text-active, --antrea-space-md
 */
export class AntreaNavGroup extends LitElement {
    static styles = css`
        :host {
            display: block;
        }

        .group-header {
            display: flex;
            align-items: stretch;
            cursor: pointer;
        }

        .group-header ::slotted(*) {
            flex: 1;
            min-width: 0;
        }

        .group-toggle {
            display: flex;
            align-items: center;
            justify-content: center;
            flex-shrink: 0;
            width: 32px;
            background: none;
            border: none;
            cursor: pointer;
            color: var(--antrea-nav-item-text, #adbbc4);
        }

        .group-header:hover .group-toggle {
            color: var(--antrea-nav-item-text-active, #e9ecef);
        }

        .toggle-icon {
            display: inline-block;
            width: 7px;
            height: 7px;
            border-right: 2px solid currentColor;
            border-bottom: 2px solid currentColor;
            transform: rotate(45deg);
            transition: transform 0.15s;
        }

        :host([expanded]) .toggle-icon {
            transform: rotate(225deg);
        }

        .group-children {
            padding-left: var(--antrea-space-md, 1rem);
        }

        .group-children.collapsed {
            display: none;
        }
    `;

    @property({ type: Boolean, reflect: true }) expanded = false;
    @property({ type: Boolean }) hasActiveChild = false;

    protected updated(changed: PropertyValues) {
        // Fires on the initial render too (Lit's first `changed` includes every reactive
        // property), so this also covers the plain mount-with-an-active-child case `firstUpdated`
        // used to handle — plus any later false-to-true transition. Only ever expands: reading
        // `this.hasActiveChild` (not the old value) rather than diffing means a true-to-false
        // transition is silently ignored, so the user's own toggle is never overridden.
        if (changed.has('hasActiveChild') && this.hasActiveChild) this.expanded = true;
    }

    private _handleToggleClick(e: Event) {
        // Stops the click from also reaching _handleHeaderClick below — this is the one control
        // allowed to collapse an expanded group.
        e.stopPropagation();
        this.expanded = !this.expanded;
    }

    private _handleHeaderClick() {
        this.expanded = true;
    }

    render() {
        return html`
            <div class="group-header" @click=${this._handleHeaderClick}>
                <slot name="header"></slot>
                <button
                    class="group-toggle"
                    part="toggle"
                    @click=${this._handleToggleClick}
                    aria-label=${this.expanded ? 'Collapse group' : 'Expand group'}
                    aria-expanded=${this.expanded}
                >
                    <span class="toggle-icon"></span>
                </button>
            </div>
            <div class="group-children ${this.expanded ? '' : 'collapsed'}">
                <slot></slot>
            </div>
        `;
    }
}

customElements.define('antrea-nav', AntreaNav);
customElements.define('antrea-nav-item', AntreaNavItem);
customElements.define('antrea-nav-group', AntreaNavGroup);

declare global {
    interface HTMLElementTagNameMap {
        'antrea-nav': AntreaNav;
        'antrea-nav-item': AntreaNavItem;
        'antrea-nav-group': AntreaNavGroup;
    }
}
