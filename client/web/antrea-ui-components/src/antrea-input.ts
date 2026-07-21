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

/**
 * A form-associated text/password input component.
 *
 * Uses ElementInternals so it participates in native <form> submission and
 * Angular Reactive Forms / React controlled forms via the `value` property.
 *
 * @fires antrea-input  - On every keystroke (detail: { value })
 * @fires antrea-change - On blur/commit (detail: { value })
 *
 * CSS tokens consumed:
 *   --antrea-color-bg-surface, --antrea-color-border, --antrea-color-border-focus,
 *   --antrea-color-text, --antrea-color-text-muted, --antrea-color-text-disabled,
 *   --antrea-color-danger, --antrea-radius-md, --antrea-font-size-base,
 *   --antrea-space-sm, --antrea-space-md
 */
export class AntreaInput extends LitElement {
    // Opt in to form association via ElementInternals.
    static formAssociated = true;

    static styles = css`
        :host {
            display: block;
        }

        .field {
            display: flex;
            flex-direction: column;
            gap: var(--antrea-space-xs, 0.25rem);
        }

        label {
            font-family: var(--antrea-font-family, sans-serif);
            font-size: var(--antrea-font-size-sm, 0.75rem);
            font-weight: var(--antrea-font-weight-medium, 500);
            color: var(--antrea-color-text-muted, #adbbc4);
        }

        input {
            width: 100%;
            box-sizing: border-box;
            padding: var(--antrea-space-sm, 0.5rem) var(--antrea-space-sm, 0.5rem);
            background: var(--antrea-color-bg-surface, #243340);
            border: 1px solid var(--antrea-color-border, #314351);
            border-radius: var(--antrea-radius-md, 4px);
            color: var(--antrea-color-text, #e9ecef);
            font-family: var(--antrea-font-family, sans-serif);
            font-size: var(--antrea-font-size-base, 0.875rem);
            transition: border-color 0.15s;
            outline: none;
        }

        input::placeholder {
            color: var(--antrea-color-text-disabled, #6a7f8e);
        }

        input:focus {
            border-color: var(--antrea-color-border-focus, #0079b8);
            box-shadow: 0 0 0 2px color-mix(in srgb, var(--antrea-color-border-focus, #0079b8) 20%, transparent);
        }

        input:disabled {
            opacity: 0.45;
            cursor: not-allowed;
        }

        :host([error]) input {
            border-color: var(--antrea-color-danger, #f54f47);
        }

        .error-msg {
            font-size: var(--antrea-font-size-sm, 0.75rem);
            color: var(--antrea-color-danger, #f54f47);
        }

        /* password toggle button */
        .input-wrapper {
            position: relative;
            display: flex;
            align-items: center;
        }

        .input-wrapper input {
            padding-right: 2.5rem;
        }

        .toggle-pw {
            position: absolute;
            right: var(--antrea-space-sm, 0.5rem);
            background: none;
            border: none;
            cursor: pointer;
            color: var(--antrea-color-text-muted, #adbbc4);
            padding: 0;
            font-size: 0.875rem;
        }
        .toggle-pw:hover { color: var(--antrea-color-text, #e9ecef); }
    `;

    @property() label = '';
    @property() placeholder = '';
    @property() value = '';
    @property({ type: Boolean, reflect: true }) disabled = false;
    @property({ type: Boolean, reflect: true }) error = false;
    @property({ attribute: 'error-message' }) errorMessage = '';
    @property() type: 'text' | 'password' | 'email' | 'number' = 'text';
    @property() name = '';
    @property() autocomplete = '';

    @query('input') private _input!: HTMLInputElement;

    private _internals: ElementInternals;
    private _showPassword = false;

    constructor() {
        super();
        this._internals = this.attachInternals();
    }

    private _handleInput(e: Event) {
        const input = e.target as HTMLInputElement;
        this.value = input.value;
        this._internals.setFormValue(this.value);
        this.dispatchEvent(new CustomEvent('antrea-input', {
            detail: { value: this.value },
            bubbles: true,
            composed: true,
        }));
    }

    private _handleChange(e: Event) {
        const input = e.target as HTMLInputElement;
        this.value = input.value;
        this._internals.setFormValue(this.value);
        this.dispatchEvent(new CustomEvent('antrea-change', {
            detail: { value: this.value },
            bubbles: true,
            composed: true,
        }));
    }

    // The inner <input> lives in this component's shadow root, so it has no form
    // owner of its own — pressing Enter triggers neither native implicit submission
    // nor antrea-button's shadow-DOM submit button (same isolation issue). Forward
    // it explicitly through the ElementInternals form association this component
    // already has.
    private _handleKeydown(e: KeyboardEvent) {
        if (e.key !== 'Enter') return;
        e.preventDefault();
        this._internals.form?.requestSubmit();
    }

    private _togglePassword() {
        this._showPassword = !this._showPassword;
        this.requestUpdate();
    }

    // Expose focus for forms
    focus() { this._input?.focus(); }

    private _resolvedType() {
        if (this.type === 'password') {
            return this._showPassword ? 'text' : 'password';
        }
        return this.type;
    }

    render() {
        const isPassword = this.type === 'password';
        return html`
            <div class="field">
                ${this.label ? html`<label for="input">${this.label}</label>` : ''}
                <div class="input-wrapper">
                    <input
                        id="input"
                        type=${this._resolvedType()}
                        name=${this.name}
                        autocomplete=${this.autocomplete}
                        .value=${this.value}
                        placeholder=${this.placeholder}
                        ?disabled=${this.disabled}
                        aria-invalid=${this.error}
                        @input=${this._handleInput}
                        @change=${this._handleChange}
                        @keydown=${this._handleKeydown}
                    />
                    ${isPassword ? html`
                        <button
                            class="toggle-pw"
                            type="button"
                            aria-label=${this._showPassword ? 'Hide password' : 'Show password'}
                            @click=${this._togglePassword}
                        >
                            ${this._showPassword ? '🙈' : '👁'}
                        </button>
                    ` : ''}
                </div>
                ${this.error && this.errorMessage ? html`
                    <span class="error-msg" role="alert">${this.errorMessage}</span>
                ` : ''}
            </div>
        `;
    }
}

customElements.define('antrea-input', AntreaInput);

declare global {
    interface HTMLElementTagNameMap {
        'antrea-input': AntreaInput;
    }
}
