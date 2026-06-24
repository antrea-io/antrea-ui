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
 * An alert/banner component for status messages.
 *
 * @slot - Alert message content
 * @fires antrea-close - When the close button is clicked (closable alerts only)
 *
 * CSS tokens consumed:
 *   --antrea-color-{danger,success,warning,info} and their -bg variants,
 *   --antrea-color-text, --antrea-color-border, --antrea-radius-md,
 *   --antrea-font-size-base, --antrea-space-sm, --antrea-space-md
 */
export class AntreaAlert extends LitElement {
    static styles = css`
        :host {
            display: block;
        }

        .alert {
            display: flex;
            align-items: flex-start;
            gap: var(--antrea-space-sm, 0.5rem);
            padding: var(--antrea-space-sm, 0.5rem) var(--antrea-space-md, 1rem);
            border-radius: var(--antrea-radius-md, 4px);
            border: 1px solid;
            font-family: var(--antrea-font-family, sans-serif);
            font-size: var(--antrea-font-size-base, 0.875rem);
            line-height: 1.5;
        }

        .alert-icon {
            flex-shrink: 0;
            font-size: 1rem;
            margin-top: 1px;
        }

        .alert-content {
            flex: 1;
        }

        .alert-close {
            flex-shrink: 0;
            background: none;
            border: none;
            cursor: pointer;
            padding: 0;
            font-size: 1rem;
            line-height: 1;
            opacity: 0.7;
            color: inherit;
        }
        .alert-close:hover { opacity: 1; }

        /* status variants */
        :host([status="danger"]) .alert {
            background: var(--antrea-color-danger-bg, #3b1f1e);
            border-color: var(--antrea-color-danger, #f54f47);
            color: var(--antrea-color-danger, #f54f47);
        }
        :host([status="success"]) .alert {
            background: var(--antrea-color-success-bg, #1e3a1a);
            border-color: var(--antrea-color-success, #60b515);
            color: var(--antrea-color-success, #60b515);
        }
        :host([status="warning"]) .alert {
            background: var(--antrea-color-warning-bg, #3b2e12);
            border-color: var(--antrea-color-warning, #f5a623);
            color: var(--antrea-color-warning, #f5a623);
        }
        :host([status="info"]), :host(:not([status])) .alert {
            background: var(--antrea-color-info-bg, #162b38);
            border-color: var(--antrea-color-info, #0079b8);
            color: var(--antrea-color-info, #0079b8);
        }

        /* loading variant — inherits info colors */
        :host([status="loading"]) .alert {
            background: var(--antrea-color-info-bg, #162b38);
            border-color: var(--antrea-color-info, #0079b8);
            color: var(--antrea-color-text-muted, #adbbc4);
        }
    `;

    @property({ reflect: true }) status: 'danger' | 'success' | 'warning' | 'info' | 'loading' = 'info';
    @property({ type: Boolean, reflect: true }) closable = false;

    private _statusIcon() {
        switch (this.status) {
            case 'danger':  return '✕';
            case 'success': return '✓';
            case 'warning': return '⚠';
            case 'loading': return '⟳';
            default:        return 'ℹ';
        }
    }

    private _handleClose() {
        this.dispatchEvent(new CustomEvent('antrea-close', { bubbles: true, composed: true }));
    }

    render() {
        return html`
            <div class="alert" role="alert">
                <span class="alert-icon" aria-hidden="true">${this._statusIcon()}</span>
                <span class="alert-content"><slot></slot></span>
                ${this.closable ? html`
                    <button class="alert-close" aria-label="Close" @click=${this._handleClose}>✕</button>
                ` : ''}
            </div>
        `;
    }
}

customElements.define('antrea-alert', AntreaAlert);

declare global {
    interface HTMLElementTagNameMap {
        'antrea-alert': AntreaAlert;
    }
}
