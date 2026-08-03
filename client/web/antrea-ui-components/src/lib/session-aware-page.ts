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

import { LitElement } from 'lit';
import { APIError } from './api.js';

/**
 * Base class for page components that call the backend API on the logged-in user's behalf.
 *
 * Authentication is the session cookie, which the browser attaches by itself, so a page has no
 * credential to receive and no arrival race to wait out: override onSessionReady() and it fires
 * once, from connectedCallback(). (This replaces the old `token` property, which the host set
 * only after the element connected, so fetching eagerly would 401.)
 *
 * For reporting a dead session: call isSessionExpiredError(e) in a catch block, and
 * dispatchSessionExpired() if it's true. The host logs the user out in response — a 401 is no
 * longer recoverable, because credential refresh happens server-side and the backend has already
 * attempted it. A 403 is NOT a session error: it means the user is logged in but lacks the
 * Kubernetes RBAC for this particular call, which is routine now that each user acts as
 * themselves.
 */
export abstract class SessionAwarePage extends LitElement {
    override connectedCallback() {
        super.connectedCallback();
        this.onSessionReady();
    }

    /** Override to start data loading. No-op by default for pages that only call the API in
     * response to a user action (e.g. on form submit). */
    protected onSessionReady(): void {}

    protected isSessionExpiredError(err: unknown): boolean {
        return err instanceof APIError && err.code === 401;
    }

    protected dispatchSessionExpired(): void {
        this.dispatchEvent(new CustomEvent('antrea-session-expired', { bubbles: true, composed: true }));
    }
}
