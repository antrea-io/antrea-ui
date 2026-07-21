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

import { Injectable, signal } from '@angular/core';

@Injectable({ providedIn: 'root' })
export class AuthService {
  // undefined: no token yet, refresh not attempted; '': refresh attempted and failed;
  // non-empty string: authenticated.
  readonly token = signal<string | undefined>(undefined);

  setToken(token: string): void {
    this.token.set(token);
  }

  /** Called when a page component reports an antrea-session-expired event (HTTP 401). */
  sessionExpired(): void {
    this.logout('Your session has expired. Please log in again.');
  }

  logout(msg?: string): void {
    this.token.set('');
    localStorage.removeItem('ui.antrea.io/use-oidc');
    let redirectURL = window.location.origin;
    if (msg) {
      const params = new URLSearchParams();
      params.set('msg', msg);
      redirectURL += `?${params.toString()}`;
    }
    const params = new URLSearchParams();
    params.set('redirect_url', redirectURL);
    window.location.href = `/auth/logout?${params.toString()}`;
  }
}
