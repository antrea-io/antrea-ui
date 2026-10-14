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
import { property, query } from 'lit/decorators.js';

/** The smallest a heading is shrunk to before it is allowed to break inside a word. */
const MIN_HEADING_PX = 10;

/**
 * A themed card container with an optional heading.
 *
 * @attr heading - Text displayed in the card header. Omit for a bare card. A heading too wide for
 *   the card shrinks to fit, down to a floor, and only then breaks inside a word.
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
            /* Lets a host given an explicit height (e.g. a stretched grid item) pass it through,
               so cards laid out side by side can be made equal height. Resolves to auto — no
               change in the usual case where the host height is content-driven. */
            height: 100%;
            box-sizing: border-box;
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
        /* The last resort, once the heading is as small as it may get: break inside a word
           rather than overflow the card. */
        .card-header.break {
            overflow-wrap: anywhere;
        }

        .card-body {
            padding: var(--antrea-space-md, 1rem);
        }
    `;

    @property() heading = '';

    @query('.card') private _card?: HTMLElement;
    @query('.card-header') private _header?: HTMLElement;
    private _resizeObserver?: ResizeObserver;

    connectedCallback() {
        super.connectedCallback();
        // Observes the card, not the header: shrinking the header's font never changes the card's
        // width, so fitting cannot retrigger itself.
        if (typeof ResizeObserver !== 'undefined') {
            this._resizeObserver = new ResizeObserver(() => this._fitHeading());
            // firstUpdated runs once per element, so a card that is disconnected and connected
            // again (moved in the DOM) has only this to observe its card with.
            if (this._card) this._resizeObserver.observe(this._card);
        }
    }

    disconnectedCallback() {
        super.disconnectedCallback();
        this._resizeObserver?.disconnect();
        this._resizeObserver = undefined;
    }

    firstUpdated() {
        if (this._card) this._resizeObserver?.observe(this._card);
    }

    updated(changed: Map<string, unknown>) {
        if (changed.has('heading')) this._fitHeading();
    }

    /** Shrinks the heading until its longest word fits the card width, from the themed size down
     * to MIN_HEADING_PX. Wrapping between words is left to the browser; only a single word wider
     * than the card counts as not fitting. */
    private _fitHeading() {
        const header = this._header;
        if (!header) return;
        header.style.fontSize = '';
        header.classList.remove('break');
        let size = parseFloat(getComputedStyle(header).fontSize);
        // No layout (not rendered yet, or no layout engine): nothing to measure.
        if (!size || header.clientWidth === 0) return;
        while (header.scrollWidth > header.clientWidth && size > MIN_HEADING_PX) {
            size = Math.max(MIN_HEADING_PX, size - 0.5);
            header.style.fontSize = `${size}px`;
        }
        if (header.scrollWidth > header.clientWidth) header.classList.add('break');
    }

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
