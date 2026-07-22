/**
 * Copyright 2026 Antrea Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import { Component, inject, signal } from '@angular/core';
import { APIError, AppSettings, apiFetchAppSettings, apiLogin, apiRefreshToken, getApiBase } from '@antrea/ui-components';
import { AuthService } from '../core/auth.service';

@Component({
  selector: 'app-login-page',
  standalone: true,
  templateUrl: './login-page.html',
  styleUrl: './login-page.css',
})
export class LoginPage {
  private readonly auth = inject(AuthService);

  readonly loading = signal(true);
  readonly settings = signal<AppSettings | null>(null);
  readonly settingsError = signal('');
  readonly loginError = signal('');
  readonly msg = signal('');
  readonly showPassword = signal(false);
  readonly submitting = signal(false);
  readonly username = signal('');
  readonly password = signal('');

  constructor() {
    this.readUrlParams();
    void this.init();
  }

  private readUrlParams(): void {
    const params = new URLSearchParams(window.location.search);

    const msg = params.get('msg');
    if (msg) this.msg.set(msg);

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

  private async init(): Promise<void> {
    const [settingsResult, refreshResult] = await Promise.allSettled([
      apiFetchAppSettings(),
      apiRefreshToken(),
    ]);

    if (settingsResult.status === 'fulfilled') {
      this.settings.set(settingsResult.value);
      // readUrlParams() ran before settings were loaded and may have unconditionally written
      // the OIDC auto-redirect flag; clear it if OIDC turns out to be disabled, so it doesn't
      // linger and trigger an unexpected auto-redirect if OIDC is enabled later.
      if (!settingsResult.value.auth.oidcEnabled) localStorage.removeItem('ui.antrea.io/use-oidc');
    } else {
      const err = settingsResult.reason;
      this.settingsError.set(err instanceof Error ? err.message : 'Failed to load settings');
    }

    if (refreshResult.status === 'fulfilled') {
      // Existing session — hand off the token and let the host navigate away.
      this.auth.setToken(refreshResult.value.accessToken);
      return;
    }

    const refreshErr = refreshResult.reason;
    if (!(refreshErr instanceof APIError && refreshErr.code === 401)) {
      this.loginError.set(refreshErr instanceof Error ? refreshErr.message : String(refreshErr));
    }

    this.loading.set(false);

    // Auto-trigger OIDC redirect if requested via URL param.
    const settings = this.settings();
    if (settings?.auth.oidcEnabled && localStorage.getItem('ui.antrea.io/use-oidc') === 'yes') {
      localStorage.removeItem('ui.antrea.io/use-oidc');
      this.doOidcLogin();
    }
  }

  togglePasswordVisibility(): void {
    this.showPassword.set(!this.showPassword());
  }

  dismissMsg(): void {
    this.msg.set('');
  }

  async onBasicSubmit(): Promise<void> {
    this.loginError.set('');
    const username = this.username();
    const password = this.password();
    if (!username || !password) {
      this.loginError.set('Username and password are required');
      return;
    }
    this.submitting.set(true);
    try {
      const token = await apiLogin(username, password);
      this.auth.setToken(token.accessToken);
    } catch (err) {
      this.loginError.set(err instanceof Error ? err.message : String(err));
      this.submitting.set(false);
    }
  }

  doOidcLogin(): void {
    const params = new URLSearchParams();
    params.set('redirect_url', window.location.href);
    window.location.href = `${getApiBase()}/auth/oauth2/login?${params.toString()}`;
  }
}
