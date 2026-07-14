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
 * A themed button component.
 *
 * @slot - Button label content
 * @csspart button - The internal <button> element, for hosts that need more
 *   styling control than the CSS tokens below expose (e.g. a downstream
 *   Clarity shell overriding text-transform/letter-spacing/height to match
 *   Clarity's own button convention).
 *
 * CSS tokens consumed:
 *   --antrea-color-primary, --antrea-color-primary-hover, --antrea-color-primary-text,
 *   --antrea-color-bg-surface, --antrea-color-border, --antrea-color-text,
 *   --antrea-color-text-disabled, --antrea-radius-md, --antrea-font-size-base,
 *   --antrea-font-weight-medium, --antrea-space-sm, --antrea-space-md,
 *   --antrea-button-padding, --antrea-button-height, --antrea-button-font-weight,
 *   --antrea-button-text-transform, --antrea-button-letter-spacing (all unset/"none"
 *   by default; a host can set these to match its own design system's button
 *   convention, e.g. Clarity's uppercase/semibold/letter-spaced buttons)
 */
export class AntreaButton extends LitElement {
    static formAssociated = true;

    private _internals: ElementInternals;

    constructor() {
        super();
        this._internals = this.attachInternals();
    }

    static styles = css`
        :host {
            display: inline-block;
        }

        button {
            display: inline-flex;
            align-items: center;
            justify-content: center;
            gap: var(--antrea-space-xs, 0.25rem);
            padding: var(--antrea-button-padding, var(--antrea-space-sm, 0.5rem) var(--antrea-space-md, 1rem));
            min-height: var(--antrea-button-height, auto);
            font-family: var(--antrea-font-family, sans-serif);
            font-size: var(--antrea-font-size-base, 0.875rem);
            font-weight: var(--antrea-button-font-weight, var(--antrea-font-weight-medium, 500));
            text-transform: var(--antrea-button-text-transform, none);
            letter-spacing: var(--antrea-button-letter-spacing, normal);
            border-radius: var(--antrea-radius-md, 4px);
            border: 1px solid transparent;
            cursor: pointer;
            transition: background 0.15s, border-color 0.15s, color 0.15s;
            white-space: nowrap;
        }

        button:disabled {
            opacity: 0.45;
            cursor: not-allowed;
        }

        /* primary (default) */
        :host(:not([action])) button,
        :host([action="solid"]) button {
            background: var(--antrea-color-primary, #0079b8);
            color: var(--antrea-color-primary-text, #fff);
            border-color: var(--antrea-color-primary, #0079b8);
        }

        :host(:not([action])) button:hover:not(:disabled),
        :host([action="solid"]) button:hover:not(:disabled) {
            background: var(--antrea-color-primary-hover, #005f8e);
            border-color: var(--antrea-color-primary-hover, #005f8e);
        }

        /* outline */
        :host([action="outline"]) button {
            background: transparent;
            color: var(--antrea-color-primary, #0079b8);
            border-color: var(--antrea-color-primary, #0079b8);
        }

        :host([action="outline"]) button:hover:not(:disabled) {
            background: var(--antrea-color-bg-hover, #2e3f4d);
        }

        /* flat */
        :host([action="flat"]) button {
            background: transparent;
            color: var(--antrea-color-primary, #0079b8);
            border-color: transparent;
        }

        :host([action="flat"]) button:hover:not(:disabled) {
            background: var(--antrea-color-bg-hover, #2e3f4d);
        }

        button:focus-visible {
            outline: 2px solid var(--antrea-color-border-focus, #0079b8);
            outline-offset: 2px;
        }
    `;

    @property({ reflect: true }) action: 'solid' | 'outline' | 'flat' = 'solid';
    @property({ type: Boolean, reflect: true }) disabled = false;
    @property({ reflect: true }) type: 'button' | 'submit' | 'reset' = 'button';

    private handleClick() {
        if (this.disabled) return;
        const form = this._internals.form;
        if (!form) return;
        if (this.type === 'submit') {
            form.requestSubmit();
        } else if (this.type === 'reset') {
            form.reset();
        }
    }

    render() {
        return html`
            <button
                part="button"
                type="button"
                ?disabled=${this.disabled}
                @click=${this.handleClick}
            >
                <slot></slot>
            </button>
        `;
    }
}

customElements.define('antrea-button', AntreaButton);

declare global {
    interface HTMLElementTagNameMap {
        'antrea-button': AntreaButton;
    }
}
