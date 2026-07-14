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

import { afterEach, describe, expect, test } from 'vitest';
import { html } from 'lit';
import { TokenAwarePage } from './token-aware-page';
import { APIError } from './api';

class TestPage extends TokenAwarePage {
    onTokenReadyCallCount = 0;

    protected override onTokenReady() {
        this.onTokenReadyCallCount++;
    }

    // Expose the protected helpers for assertions.
    checkIsSessionExpiredError(err: unknown) {
        return this.isSessionExpiredError(err);
    }

    triggerSessionExpired() {
        this.dispatchSessionExpired();
    }

    override render() {
        return html`<p>token: ${this.token}</p>`;
    }
}
customElements.define('test-token-aware-page', TestPage);

let el: TestPage;

afterEach(() => {
    el?.remove();
});

async function mount(): Promise<TestPage> {
    el = document.createElement('test-token-aware-page') as TestPage;
    document.body.appendChild(el);
    await el.updateComplete;
    return el;
}

describe('TokenAwarePage', () => {
    test('does not call onTokenReady while the token is empty', async () => {
        const page = await mount();
        expect(page.onTokenReadyCallCount).toBe(0);
    });

    test('calls onTokenReady once the token first becomes non-empty', async () => {
        const page = await mount();
        page.token = 'my-token';
        await page.updateComplete;
        expect(page.onTokenReadyCallCount).toBe(1);
    });

    test('calls onTokenReady again on a subsequent token change (e.g. refresh)', async () => {
        const page = await mount();
        page.token = 'my-token';
        await page.updateComplete;
        page.token = 'refreshed-token';
        await page.updateComplete;
        expect(page.onTokenReadyCallCount).toBe(2);
    });

    test('does not call onTokenReady when the token is cleared back to empty', async () => {
        const page = await mount();
        page.token = 'my-token';
        await page.updateComplete;
        page.token = '';
        await page.updateComplete;
        expect(page.onTokenReadyCallCount).toBe(1);
    });

    test('isSessionExpiredError is true only for a 401 APIError', async () => {
        const page = await mount();
        expect(page.checkIsSessionExpiredError(new APIError(401, 'Unauthorized', 'expired'))).toBe(true);
        expect(page.checkIsSessionExpiredError(new APIError(403, 'Forbidden', 'denied'))).toBe(false);
        expect(page.checkIsSessionExpiredError(new Error('network error'))).toBe(false);
    });

    test('dispatchSessionExpired fires a bubbling, composed antrea-session-expired event', async () => {
        const page = await mount();
        let received: Event | undefined;
        document.body.addEventListener('antrea-session-expired', e => { received = e; });

        page.triggerSessionExpired();

        expect(received).toBeDefined();
        expect(received?.bubbles).toBe(true);
        expect(received?.composed).toBe(true);
    });
});
