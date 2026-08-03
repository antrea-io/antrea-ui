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

import { act, render, waitFor } from '@testing-library/react';
import { Provider } from 'react-redux';
import { setupStore } from './store';
import { SummaryPage } from './pages';

// AntreaSummaryPage is a Lit web component with its own shadow DOM; we only need
// its host element here to dispatch the antrea-session-expired event.

function stubLocationHref() {
    const hrefSetter = vi.fn();
    const originalLocation = Object.getOwnPropertyDescriptor(window, 'location');
    Object.defineProperty(window, 'location', {
        value: new Proxy(window.location, {
            set(target, prop, value) {
                if (prop === 'href') { hrefSetter(value); return true; }
                return Reflect.set(target, prop, value);
            },
        }),
        configurable: true,
    });
    return {
        hrefSetter,
        restore: () => { if (originalLocation) Object.defineProperty(window, 'location', originalLocation); },
    };
}

afterEach(() => {
    vi.unstubAllGlobals();
});

describe('useLitPage — antrea-session-expired', () => {
    // There is no probe-then-retry any more. Credential refresh happens server-side, and the
    // backend has already attempted the only refresh that exists, so a 401 is authoritative:
    // the session is gone and the user has to log in again.
    test('logs the user out immediately, without probing the backend first', async () => {
        const store = setupStore({ session: 'authenticated' });
        const fetchMock = vi.fn(async (url: string) => {
            throw new Error(`unexpected fetch to ${url}`);
        });
        vi.stubGlobal('fetch', fetchMock);
        const location = stubLocationHref();

        try {
            render(<Provider store={store}><SummaryPage /></Provider>);
            const el = document.querySelector('antrea-summary-page')!;

            await act(async () => {
                el.dispatchEvent(new CustomEvent('antrea-session-expired'));
            });

            await waitFor(() => expect(store.getState().session).toBe('anonymous'));
            expect(location.hrefSetter).toHaveBeenCalledTimes(1);
            const redirect = location.hrefSetter.mock.calls[0][0] as string;
            expect(redirect).toContain('/auth/logout?');
            // The message is nested inside the redirect_url parameter, hence double-encoded.
            expect(decodeURIComponent(redirect)).toContain('session+has+expired');
            // No /auth/* round-trip: the old code tried a token refresh here first.
            expect(fetchMock.mock.calls.filter(([url]) => url.startsWith('/auth/'))).toHaveLength(0);
        } finally {
            location.restore();
        }
    });
});
