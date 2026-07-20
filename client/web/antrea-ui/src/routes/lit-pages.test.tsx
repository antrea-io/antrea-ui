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
import { setupStore } from '../store';
import { SummaryPage } from './lit-pages';

// AntreaSummaryPage is a Lit web component with its own shadow DOM; we only need
// its host element here to dispatch the antrea-session-expired event.

function jsonResponse(body: unknown, status = 200): Response {
    return new Response(JSON.stringify(body), { status });
}

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

describe('useLitPage — silent refresh on antrea-session-expired', () => {
    test('401 -> refresh succeeds -> the store token is replaced and the page is not logged out', async () => {
        const store = setupStore({ token: 'stale-token' });
        vi.stubGlobal('fetch', vi.fn(async (url: string) => {
            if (url === '/auth/refresh_token') {
                return jsonResponse({ tokenType: 'Bearer', accessToken: 'fresh-token', expiresIn: 3600 });
            }
            throw new Error(`unexpected fetch to ${url}`);
        }));
        const location = stubLocationHref();

        try {
            render(<Provider store={store}><SummaryPage /></Provider>);
            const el = document.querySelector('antrea-summary-page')!;

            await act(async () => {
                el.dispatchEvent(new CustomEvent('antrea-session-expired'));
            });

            await waitFor(() => expect(store.getState().token).toBe('fresh-token'));
            expect(location.hrefSetter).not.toHaveBeenCalled();
        } finally {
            location.restore();
        }
    });

    test('401 -> refresh 401s -> the user is logged out', async () => {
        const store = setupStore({ token: 'stale-token' });
        vi.stubGlobal('fetch', vi.fn(async (url: string) => {
            if (url === '/auth/refresh_token') {
                return new Response('refresh cookie expired', { status: 401 });
            }
            throw new Error(`unexpected fetch to ${url}`);
        }));
        const location = stubLocationHref();

        try {
            render(<Provider store={store}><SummaryPage /></Provider>);
            const el = document.querySelector('antrea-summary-page')!;

            await act(async () => {
                el.dispatchEvent(new CustomEvent('antrea-session-expired'));
            });

            await waitFor(() => expect(store.getState().token).toBe(''));
            expect(location.hrefSetter).toHaveBeenCalledTimes(1);
            expect(location.hrefSetter.mock.calls[0][0]).toContain('/auth/logout?');
        } finally {
            location.restore();
        }
    });
});
