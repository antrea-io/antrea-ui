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

customElements.define('antrea-nav', AntreaNav);
customElements.define('antrea-nav-item', AntreaNavItem);

declare global {
    interface HTMLElementTagNameMap {
        'antrea-nav': AntreaNav;
        'antrea-nav-item': AntreaNavItem;
    }
}
