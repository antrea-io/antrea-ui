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

import { html, nothing } from 'lit';
import { state, query } from 'lit/decorators.js';
import { pageStyles } from '../lib/styles.js';
import { apiFetch } from '../lib/api.js';
import { TokenAwarePage } from '../lib/token-aware-page.js';
import '../antrea-button';
import '../antrea-alert';
import '../antrea-card';

export class AntreaSettingsPage extends TokenAwarePage {
    static styles = pageStyles;

    @state() private _loading = false;
    @state() private _success = false;
    @state() private _error = '';
    @state() private _fieldErrors: Record<string, string> = {};

    @query('#current-password') private _currentPw!: HTMLInputElement;
    @query('#new-password') private _newPw!: HTMLInputElement;
    @query('#confirm-password') private _confirmPw!: HTMLInputElement;

    private _validate(): boolean {
        const errors: Record<string, string> = {};
        if (!this._currentPw.value) errors['current'] = 'Current password is required';
        if (!this._newPw.value) errors['new'] = 'New password is required';
        else if (this._newPw.value.length < 8) errors['new'] = 'Password must be at least 8 characters';
        if (this._newPw.value !== this._confirmPw.value) errors['confirm'] = 'Passwords do not match';
        this._fieldErrors = errors;
        return Object.keys(errors).length === 0;
    }

    private async _submit(e: Event) {
        e.preventDefault();
        if (!this._validate()) return;

        this._loading = true;
        this._success = false;
        this._error = '';
        try {
            await apiFetch('account/password', this.token, {
                method: 'PUT',
                headers: { 'content-type': 'application/json' },
                body: JSON.stringify({
                    currentPassword: btoa(this._currentPw.value),
                    newPassword: btoa(this._newPw.value),
                }),
            });
            this._success = true;
            this._currentPw.value = '';
            this._newPw.value = '';
            this._confirmPw.value = '';
        } catch (e) {
            if (this.isSessionExpiredError(e)) {
                this.dispatchSessionExpired();
                return;
            }
            this._error = e instanceof Error ? e.message : String(e);
        } finally {
            this._loading = false;
        }
    }

    private _field(id: string, label: string, type = 'password') {
        const errorKey = id === 'current-password' ? 'current' : id === 'new-password' ? 'new' : 'confirm';
        const err = this._fieldErrors[errorKey];
        const autocomplete = id === 'current-password' ? 'current-password' : 'new-password';
        return html`
            <div class="field-group">
                <label class="field-label" for=${id}>${label}</label>
                <input id=${id} class="field-input ${err ? 'error' : ''}" type=${type} autocomplete=${autocomplete} />
                ${err ? html`<span class="field-error">${err}</span>` : nothing}
            </div>
        `;
    }

    override render() {
        return html`
            <main>
                <div class="page-layout">
                    <p class="page-title">Settings</p>

                    <antrea-card heading="Change Password">
                        ${this._success ? html`<antrea-alert status="success">Password changed successfully.</antrea-alert>` : nothing}
                        ${this._error ? html`<antrea-alert status="danger">${this._error}</antrea-alert>` : nothing}
                        <form class="form-stack" @submit=${this._submit}>
                            ${this._field('current-password', 'Current Password')}
                            ${this._field('new-password', 'New Password')}
                            ${this._field('confirm-password', 'Confirm New Password')}
                            <div class="btn-group">
                                <antrea-button type="submit" ?disabled=${this._loading}>
                                    ${this._loading ? 'Saving…' : 'Change Password'}
                                </antrea-button>
                            </div>
                        </form>
                    </antrea-card>
                </div>
            </main>
        `;
    }
}

customElements.define('antrea-settings-page', AntreaSettingsPage);

declare global {
    interface HTMLElementTagNameMap { 'antrea-settings-page': AntreaSettingsPage; }
}
