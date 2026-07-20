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

import { LitElement, html, css, nothing } from 'lit';
import { state, query } from 'lit/decorators.js';
import { pageStyles } from '../lib/styles.js';
import { APIError, getApiBase } from '../lib/api.js';
import { Token, AppSettings, apiLogin, apiRefreshToken, apiFetchAppSettings } from '../lib/auth-api.js';
import '../antrea-button.js';
import '../antrea-alert.js';

export class AntreaLoginPage extends LitElement {
    static styles = [
        pageStyles,
        css`
            :host {
                display: flex;
                justify-content: center;
                padding-top: 80px;
                width: 100%;
            }
            .login-wall {
                width: 100%;
                max-width: 400px;
                display: flex;
                flex-direction: column;
                gap: var(--antrea-space-lg, 1.5rem);
            }
            h2 {
                margin: 0;
                font-size: var(--antrea-font-size-heading, 1.25rem);
                font-weight: var(--antrea-font-weight-bold, 600);
                color: var(--antrea-color-text, #e9ecef);
            }
            .login-form {
                display: flex;
                flex-direction: column;
                gap: var(--antrea-space-md, 1rem);
            }
        `,
    ];

    @state() private _loading = true;
    @state() private _settings: AppSettings | null = null;
    @state() private _settingsError = '';
    @state() private _loginError = '';
    @state() private _msg = '';
    // true after we dispatched antrea-token — show spinner until the host unmounts us
    @state() private _tokenDispatched = false;

    @query('#username') private _usernameEl?: HTMLInputElement;
    @query('#password') private _passwordEl?: HTMLInputElement;

    override connectedCallback() {
        super.connectedCallback();
        this._readUrlParams();
        this._init();
    }

    private _readUrlParams() {
        const params = new URLSearchParams(window.location.search);

        const msg = params.get('msg');
        if (msg) this._msg = msg;

        const authMethod = params.get('auth_method');
        if (authMethod) {
            if (authMethod === 'oidc') {
                localStorage.setItem('ui.antrea.io/use-oidc', 'yes');
            }
            const url = new URL(window.location.href);
            url.searchParams.delete('auth_method');
            window.history.replaceState({}, '', url.toString());
        }
    }

    private async _init() {
        const [settingsResult, refreshResult] = await Promise.allSettled([
            apiFetchAppSettings(),
            apiRefreshToken(),
        ]);

        if (settingsResult.status === 'fulfilled') {
            this._settings = settingsResult.value;
            // _readUrlParams() ran before settings were loaded and may have unconditionally
            // written the OIDC auto-redirect flag; clear it if OIDC turns out to be disabled,
            // so it doesn't linger and trigger an unexpected auto-redirect if OIDC is enabled
            // later.
            if (!this._settings.auth.oidcEnabled) localStorage.removeItem('ui.antrea.io/use-oidc');
        } else {
            const err = settingsResult.reason;
            this._settingsError = err instanceof Error ? err.message : 'Failed to load settings';
        }

        if (refreshResult.status === 'fulfilled') {
            // Existing session — dispatch token and wait for host to navigate away
            this._tokenDispatched = true;
            this._dispatchToken(refreshResult.value);
            return;
        }

        const refreshErr = refreshResult.reason;
        if (!(refreshErr instanceof APIError && refreshErr.code === 401)) {
            this._loginError = refreshErr instanceof Error ? refreshErr.message : String(refreshErr);
        }

        this._loading = false;

        // Auto-trigger OIDC redirect if requested via URL param
        if (this._settings?.auth.oidcEnabled && localStorage.getItem('ui.antrea.io/use-oidc') === 'yes') {
            localStorage.removeItem('ui.antrea.io/use-oidc');
            this._doOidcLogin();
        }
    }

    private _dispatchToken(token: Token) {
        this.dispatchEvent(new CustomEvent('antrea-token', {
            detail: { accessToken: token.accessToken },
            bubbles: true,
            composed: true,
        }));
    }

    private async _onBasicSubmit(e: Event) {
        e.preventDefault();
        this._loginError = '';
        const username = this._usernameEl?.value ?? '';
        const password = this._passwordEl?.value ?? '';
        try {
            const token = await apiLogin(username, password);
            this._tokenDispatched = true;
            this._dispatchToken(token);
        } catch (err) {
            this._loginError = err instanceof Error ? err.message : String(err);
        }
    }

    private _doOidcLogin() {
        const params = new URLSearchParams();
        params.set('redirect_url', window.location.href);
        window.location.href = `${getApiBase()}/auth/oauth2/login?${params.toString()}`;
    }

    private _renderBasicForm() {
        return html`
            <form class="login-form" @submit=${this._onBasicSubmit}>
                <div class="field-group">
                    <label class="field-label" for="username">Username</label>
                    <input id="username" class="field-input" type="text" placeholder="admin" autocomplete="username" />
                </div>
                <div class="field-group">
                    <label class="field-label" for="password">Password</label>
                    <input id="password" class="field-input" type="password" autocomplete="current-password" />
                </div>
                <div class="btn-group">
                    <antrea-button type="submit">Login</antrea-button>
                </div>
            </form>
        `;
    }

    private _renderOidcButton() {
        const name = this._settings?.auth.oidcProviderName ?? 'OIDC';
        return html`
            <div class="btn-group">
                <antrea-button action="outline" @click=${this._doOidcLogin}>
                    Login with ${name}
                </antrea-button>
            </div>
        `;
    }

    override render() {
        if (this._loading || this._tokenDispatched) {
            return html`
                <div class="loading-row">
                    <div class="spinner" role="status" aria-label="Authenticating"></div>
                    <p>Authenticating...</p>
                </div>
            `;
        }

        if (this._settingsError && !this._settings) {
            return html`<antrea-alert status="danger">${this._settingsError}</antrea-alert>`;
        }

        return html`
            <div class="login-wall">
                <h2>Please log in</h2>
                ${this._msg ? html`
                    <antrea-alert status="success" closable @antrea-close=${() => { this._msg = ''; }}>
                        ${this._msg}
                    </antrea-alert>
                ` : nothing}
                ${this._loginError ? html`<antrea-alert status="danger">${this._loginError}</antrea-alert>` : nothing}
                ${this._settings?.auth.basicEnabled ? this._renderBasicForm() : nothing}
                ${this._settings?.auth.oidcEnabled ? this._renderOidcButton() : nothing}
            </div>
        `;
    }
}

customElements.define('antrea-login-page', AntreaLoginPage);

declare global {
    interface HTMLElementTagNameMap { 'antrea-login-page': AntreaLoginPage; }
}
