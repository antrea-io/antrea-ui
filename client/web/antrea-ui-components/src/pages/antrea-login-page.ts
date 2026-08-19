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
import {
    AppSettings,
    SessionInfo,
    apiLogin,
    apiLoginWithToken,
    apiLoginWithKubeconfig,
    apiSession,
    apiFetchAppSettings,
} from '../lib/auth-api.js';
import '../antrea-button.js';
import '../antrea-alert.js';
import '../antrea-input.js';
import type { AntreaInput } from '../antrea-input.js';

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
            .method {
                display: flex;
                flex-direction: column;
                gap: var(--antrea-space-sm, 0.5rem);
            }
            .method-label {
                font-family: var(--antrea-font-family, sans-serif);
                font-size: var(--antrea-font-size-sm, 0.75rem);
                font-weight: var(--antrea-font-weight-medium, 500);
                color: var(--antrea-color-text-muted, #adbbc4);
            }
            .hint {
                margin: 0;
                font-family: var(--antrea-font-family, sans-serif);
                font-size: var(--antrea-font-size-sm, 0.75rem);
                color: var(--antrea-color-text-muted, #adbbc4);
            }
            textarea {
                width: 100%;
                box-sizing: border-box;
                min-height: 8rem;
                padding: var(--antrea-space-sm, 0.5rem);
                background: var(--antrea-color-bg-surface, #243340);
                border: 1px solid var(--antrea-color-border, #314351);
                border-radius: var(--antrea-radius-md, 3px);
                color: var(--antrea-color-text, #e9ecef);
                font-family: var(--antrea-font-family-mono, monospace);
                font-size: var(--antrea-font-size-sm, 0.75rem);
                resize: vertical;
            }
            textarea:focus {
                outline: none;
                border-color: var(--antrea-color-border-focus, #4aaed9);
            }
            .separator {
                display: flex;
                align-items: center;
                gap: var(--antrea-space-sm, 0.5rem);
                color: var(--antrea-color-text-muted, #adbbc4);
                font-family: var(--antrea-font-family, sans-serif);
                font-size: var(--antrea-font-size-sm, 0.75rem);
            }
            .separator::before,
            .separator::after {
                content: '';
                flex: 1;
                height: 1px;
                background: var(--antrea-color-border, #314351);
            }
        `,
    ];

    @state() private _loading = true;
    @state() private _settings: AppSettings | null = null;
    @state() private _settingsError = '';
    @state() private _loginError = '';
    @state() private _msg = '';
    // true after we dispatched antrea-authenticated — show spinner until the host unmounts us
    @state() private _authenticated = false;
    @state() private _submitting = false;

    @query('#username') private _usernameEl?: AntreaInput;
    @query('#password') private _passwordEl?: AntreaInput;
    @query('#sa-token') private _tokenEl?: HTMLTextAreaElement;
    @query('#kubeconfig') private _kubeconfigEl?: HTMLTextAreaElement;

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
        const [settingsResult, sessionResult] = await Promise.allSettled([
            apiFetchAppSettings(),
            apiSession(),
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

        if (sessionResult.status === 'fulfilled') {
            // Existing session — tell the host and wait for it to navigate away. We already have
            // the session info from this probe, so the host doesn't need a round-trip of its own
            // just to display who is logged in.
            this._dispatchAuthenticated(sessionResult.value);
            return;
        }

        // A 401 here just means "not logged in", which is the expected answer on a fresh visit.
        const sessionErr = sessionResult.reason;
        if (!(sessionErr instanceof APIError && sessionErr.code === 401)) {
            this._loginError = sessionErr instanceof Error ? sessionErr.message : String(sessionErr);
        }

        this._loading = false;

        // Auto-trigger OIDC redirect if requested via URL param
        if (this._settings?.auth.oidcEnabled && localStorage.getItem('ui.antrea.io/use-oidc') === 'yes') {
            localStorage.removeItem('ui.antrea.io/use-oidc');
            this._doOidcLogin();
        }
    }

    // info is display-only: the credential itself never crosses this boundary, it lives in an
    // HttpOnly cookie the host never sees. undefined when the login succeeded but a subsequent
    // GET /auth/session to fetch it did not — the host just shows nothing extra in that case.
    private _dispatchAuthenticated(info?: SessionInfo) {
        this._authenticated = true;
        this.dispatchEvent(new CustomEvent('antrea-authenticated', { bubbles: true, composed: true, detail: info }));
    }

    /** Runs a login call, surfacing its error rather than letting it reject. */
    private async _submit(login: () => Promise<void>) {
        this._loginError = '';
        this._submitting = true;
        try {
            await login();
            // The login response itself carries no session info (just a Set-Cookie), so fetch it
            // for the host to display — best-effort, a failure here must not undo a successful
            // login.
            const info = await apiSession().catch(() => undefined);
            this._dispatchAuthenticated(info);
        } catch (err) {
            this._loginError = err instanceof Error ? err.message : String(err);
        } finally {
            this._submitting = false;
        }
    }

    private async _onBasicSubmit(e: Event) {
        e.preventDefault();
        const username = this._usernameEl?.value ?? '';
        const password = this._passwordEl?.value ?? '';
        if (!username || !password) {
            this._loginError = 'Username and password are required';
            return;
        }
        await this._submit(() => apiLogin(username, password));
    }

    private async _onTokenSubmit(e: Event) {
        e.preventDefault();
        const token = this._tokenEl?.value.trim() ?? '';
        if (!token) {
            this._loginError = 'A token is required';
            return;
        }
        await this._submit(() => apiLoginWithToken(token));
    }

    private async _onKubeconfigSubmit(e: Event) {
        e.preventDefault();
        const kubeconfig = this._kubeconfigEl?.value ?? '';
        if (!kubeconfig.trim()) {
            this._loginError = 'A kubeconfig is required';
            return;
        }
        await this._submit(() => apiLoginWithKubeconfig(kubeconfig));
    }

    /** Reads an uploaded kubeconfig into the textarea, so the user can review it before sending. */
    private async _onKubeconfigFile(e: Event) {
        const input = e.target as HTMLInputElement;
        const file = input.files?.[0];
        if (!file) return;
        try {
            const text = await file.text();
            if (this._kubeconfigEl) this._kubeconfigEl.value = text;
        } catch (err) {
            this._loginError = err instanceof Error ? err.message : String(err);
        } finally {
            // Allow re-selecting the same file after an edit.
            input.value = '';
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
                <antrea-input id="username" label="Username" type="text" placeholder="admin" autocomplete="username"></antrea-input>
                <antrea-input id="password" label="Password" type="password" autocomplete="current-password"></antrea-input>
                <div class="btn-group">
                    <antrea-button type="submit" ?disabled=${this._submitting}>Login</antrea-button>
                </div>
            </form>
        `;
    }

    private _renderOidcButton() {
        const name = this._settings?.auth.oidcProviderName ?? 'OIDC';
        return html`
            <div class="btn-group">
                <antrea-button action="outline" ?disabled=${this._submitting} @click=${this._doOidcLogin}>
                    Login with ${name}
                </antrea-button>
            </div>
        `;
    }

    private _renderTokenForm() {
        return html`
            <form class="method" @submit=${this._onTokenSubmit}>
                <label class="method-label" for="sa-token">Kubernetes token</label>
                <textarea id="sa-token" spellcheck="false" autocomplete="off"
                    placeholder="eyJhbGciOiJSUzI1NiIsImtpZCI6..."></textarea>
                <p class="hint">
                    Antrea UI will act as the identity this token belongs to, so what you can see
                    and do is decided by that identity's Kubernetes RBAC. Generate one with
                    <code>kubectl create token &lt;serviceaccount&gt;</code>.
                </p>
                <div class="btn-group">
                    <antrea-button type="submit" action="outline" ?disabled=${this._submitting}>
                        Login with token
                    </antrea-button>
                </div>
            </form>
        `;
    }

    private _renderKubeconfigForm() {
        return html`
            <form class="method" @submit=${this._onKubeconfigSubmit}>
                <label class="method-label" for="kubeconfig">Kubeconfig</label>
                <textarea id="kubeconfig" spellcheck="false" autocomplete="off"
                    placeholder="apiVersion: v1&#10;kind: Config&#10;..."></textarea>
                <input type="file" accept=".yaml,.yml,.conf,.kubeconfig,text/*" @change=${this._onKubeconfigFile}>
                <p class="hint">
                    Only the current context's credential is kept, and only in memory for the
                    duration of your session. Credentials that run a program on your machine
                    (<code>exec</code> plugins, <code>auth-provider</code>) and references to local
                    files are not supported — use
                    <code>kubectl config view --raw --minify</code> to produce a self-contained
                    kubeconfig.
                </p>
                <div class="btn-group">
                    <antrea-button type="submit" action="outline" ?disabled=${this._submitting}>
                        Login with kubeconfig
                    </antrea-button>
                </div>
            </form>
        `;
    }

    override render() {
        if (this._loading || this._authenticated) {
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

        const auth = this._settings?.auth;
        // The alternative methods are grouped below a separator so the primary (password or
        // OIDC) stays the obvious one.
        const hasPrimary = Boolean(auth?.basicEnabled || auth?.oidcEnabled);
        const hasAlternative = Boolean(auth?.serviceAccountTokenEnabled || auth?.kubeconfigEnabled);

        return html`
            <div class="login-wall">
                <h2>Please log in</h2>
                ${this._msg ? html`
                    <antrea-alert status="success" closable @antrea-close=${() => { this._msg = ''; }}>
                        ${this._msg}
                    </antrea-alert>
                ` : nothing}
                ${this._loginError ? html`<antrea-alert status="danger">${this._loginError}</antrea-alert>` : nothing}
                ${auth?.basicEnabled ? this._renderBasicForm() : nothing}
                ${auth?.oidcEnabled ? this._renderOidcButton() : nothing}
                ${hasPrimary && hasAlternative ? html`<div class="separator">or</div>` : nothing}
                ${auth?.serviceAccountTokenEnabled ? this._renderTokenForm() : nothing}
                ${auth?.kubeconfigEnabled ? this._renderKubeconfigForm() : nothing}
            </div>
        `;
    }
}

customElements.define('antrea-login-page', AntreaLoginPage);

declare global {
    interface HTMLElementTagNameMap { 'antrea-login-page': AntreaLoginPage; }
}
