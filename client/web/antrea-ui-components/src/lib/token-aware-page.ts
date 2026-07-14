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

import { LitElement, PropertyValues } from 'lit';
import { property } from 'lit/decorators.js';
import { APIError } from './api';

/**
 * Base class for page components that receive an auth `token` property from
 * the host and call the backend API through it.
 *
 * Handles two concerns every such page needs:
 * - The token-arrival race: hosts (e.g. Angular) set `token` only after the
 *   element connects to the DOM, so eagerly fetching in connectedCallback()
 *   would use an empty token, get a 401, and spuriously dispatch
 *   antrea-session-expired before the real token even arrives. Override
 *   onTokenReady() instead of fetching in connectedCallback()/updated() —
 *   it fires once the token first becomes non-empty, and again on every
 *   subsequent change (e.g. a token refresh).
 * - Reporting an expired session consistently: call isSessionExpiredError(e)
 *   in a catch block, and dispatchSessionExpired() if it's true, so the host
 *   can refresh the token and re-set the `token` property.
 */
export abstract class TokenAwarePage extends LitElement {
    @property() token = '';

    override updated(changed: PropertyValues) {
        super.updated(changed);
        if (changed.has('token') && this.token) this.onTokenReady();
    }

    /** Override to (re)start data loading once `token` is available. No-op by default for pages that only use the token lazily (e.g. on form submit). */
    protected onTokenReady(): void {}

    protected isSessionExpiredError(err: unknown): boolean {
        return err instanceof APIError && err.code === 401;
    }

    protected dispatchSessionExpired(): void {
        this.dispatchEvent(new CustomEvent('antrea-session-expired', { bubbles: true, composed: true }));
    }
}
