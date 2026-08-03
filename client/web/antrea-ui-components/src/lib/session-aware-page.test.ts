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
import { SessionAwarePage } from './session-aware-page';
import { APIError } from './api';

class TestPage extends SessionAwarePage {
    onSessionReadyCallCount = 0;

    protected override onSessionReady() {
        this.onSessionReadyCallCount++;
    }

    // Expose the protected helpers for assertions.
    checkIsSessionExpiredError(err: unknown) {
        return this.isSessionExpiredError(err);
    }

    triggerSessionExpired() {
        this.dispatchSessionExpired();
    }

    override render() {
        return html`<p>ready: ${this.onSessionReadyCallCount}</p>`;
    }
}
customElements.define('test-session-aware-page', TestPage);

let el: TestPage;

afterEach(() => {
    el?.remove();
});

async function mount(): Promise<TestPage> {
    el = document.createElement('test-session-aware-page') as TestPage;
    document.body.appendChild(el);
    await el.updateComplete;
    return el;
}

describe('SessionAwarePage', () => {
    // There is no credential to wait for any more: the browser sends the session cookie itself,
    // so a page can fetch as soon as it is in the DOM.
    test('calls onSessionReady once on connect', async () => {
        const page = await mount();
        expect(page.onSessionReadyCallCount).toBe(1);
    });

    test('calls onSessionReady again if the element is re-attached', async () => {
        const page = await mount();
        page.remove();
        document.body.appendChild(page);
        await page.updateComplete;
        expect(page.onSessionReadyCallCount).toBe(2);
    });

    // A 403 means the user is logged in but lacks the Kubernetes RBAC for this call, which is
    // routine now that each user acts as themselves. It must never be mistaken for a dead
    // session, or an ordinary permissions error would log the user out.
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
