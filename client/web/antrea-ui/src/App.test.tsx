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

import { act, fireEvent, render, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import App from './App';
import { store, setToken } from './store';

// AntreaLoginPage/AntreaButton are Lit web components with their own shadow DOM — Testing
// Library's screen queries don't pierce shadow roots, so assertions below query the DOM
// directly (document.querySelector) instead of using screen.getByText()/etc.

function jsonResponse(body: unknown, status = 200): Response {
    return new Response(JSON.stringify(body), { status });
}

const defaultSettings = {
    version: 'v1.0.0',
    auth: { basicEnabled: true, oidcEnabled: false },
};

afterEach(() => {
    act(() => { store.dispatch(setToken(undefined)); });
    vi.unstubAllGlobals();
});

describe('App', () => {
    test('login: an existing session hides the login page and shows the nav', async () => {
        vi.stubGlobal('fetch', vi.fn(async (url: string) => {
            if (url === '/api/v1/settings') return jsonResponse(defaultSettings);
            if (url === '/auth/refresh_token') {
                return jsonResponse({ tokenType: 'Bearer', accessToken: 'my-token', expiresIn: 3600 });
            }
            throw new Error(`unexpected fetch to ${url}`);
        }));

        render(<App />, { wrapper: MemoryRouter });

        await waitFor(() => expect(document.querySelector('antrea-login-page')).toBeNull());
        expect(store.getState().token).toBe('my-token');
    });

    test('logout: clicking Logout clears the token and shows the login page again', async () => {
        vi.stubGlobal('fetch', vi.fn(async (url: string) => {
            if (url === '/api/v1/settings') return jsonResponse(defaultSettings);
            if (url === '/auth/refresh_token') {
                return jsonResponse({ tokenType: 'Bearer', accessToken: 'my-token', expiresIn: 3600 });
            }
            throw new Error(`unexpected fetch to ${url}`);
        }));
        // useLogout() navigates via window.location.href — intercept the setter only, so
        // jsdom doesn't attempt a real navigation.
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

        try {
            render(<App />, { wrapper: MemoryRouter });
            await waitFor(() => expect(document.querySelector('antrea-login-page')).toBeNull());

            const logoutButton = document.querySelector('antrea-button')!;
            fireEvent.click(logoutButton);

            await waitFor(() => expect(document.querySelector('antrea-login-page')).not.toBeNull());
            expect(store.getState().token).toBe('');
            expect(hrefSetter).toHaveBeenCalledTimes(1);
            expect(hrefSetter.mock.calls[0][0]).toContain('/auth/logout?');
        } finally {
            if (originalLocation) Object.defineProperty(window, 'location', originalLocation);
        }
    });
});
