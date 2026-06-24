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
 * A themed card container with an optional heading.
 *
 * @attr heading - Text displayed in the card header. Omit for a bare card.
 * @slot - Card body content.
 *
 * CSS tokens consumed:
 *   --antrea-color-bg-surface, --antrea-color-border,
 *   --antrea-color-text, --antrea-color-text-muted,
 *   --antrea-font-size-base, --antrea-font-weight-medium,
 *   --antrea-space-sm, --antrea-space-md, --antrea-radius-md
 */
export class AntreaCard extends LitElement {
    static styles = css`
        :host {
            display: block;
        }

        .card {
            background: var(--antrea-color-bg-surface, #243340);
            border: 1px solid var(--antrea-color-border, #314351);
            border-radius: var(--antrea-radius-md, 4px);
            overflow: hidden;
        }

        .card-header {
            padding: var(--antrea-space-sm, 0.5rem) var(--antrea-space-md, 1rem);
            font-family: var(--antrea-font-family, sans-serif);
            font-size: var(--antrea-font-size-base, 0.875rem);
            font-weight: var(--antrea-font-weight-medium, 500);
            color: var(--antrea-color-text-muted, #adbbc4);
            border-bottom: 1px solid var(--antrea-color-border, #314351);
            text-transform: uppercase;
            letter-spacing: 0.05em;
        }

        .card-body {
            padding: var(--antrea-space-md, 1rem);
        }
    `;

    @property() heading = '';

    render() {
        return html`
            <div class="card">
                ${this.heading ? html`<div class="card-header">${this.heading}</div>` : ''}
                <div class="card-body">
                    <slot></slot>
                </div>
            </div>
        `;
    }
}

customElements.define('antrea-card', AntreaCard);

declare global {
    interface HTMLElementTagNameMap {
        'antrea-card': AntreaCard;
    }
}
