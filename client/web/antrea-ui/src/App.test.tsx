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

import type { Mock } from 'vitest';
import { act, fireEvent, render, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import App from './App';
import { store, setSession } from './store';

// AntreaLoginPage/AntreaButton are Lit web components with their own shadow DOM — Testing
// Library's screen queries don't pierce shadow roots, so assertions below query the DOM
// directly (document.querySelector) instead of using screen.getByText()/etc.

function jsonResponse(body: unknown, status = 200): Response {
    return new Response(JSON.stringify(body), { status });
}

const defaultSettings = {
    version: 'v1.0.0',
    auth: {
        basicEnabled: true,
        oidcEnabled: false,
        kubeconfigEnabled: false,
        tokenEnabled: true,
    },
};

function stubFetchWithSession(authenticated: boolean, session: { mode: string, username: string } = { mode: 'admin', username: 'admin' }) {
    const fetchMock = vi.fn(async (url: string) => {
        if (url === '/api/v1/settings') return jsonResponse(defaultSettings);
        if (url === '/auth/session') {
            return authenticated
                ? jsonResponse({ authenticated: true, ...session })
                : new Response('not logged in', { status: 401 });
        }
        throw new Error(`unexpected fetch to ${url}`);
    });
    vi.stubGlobal('fetch', fetchMock);
    return fetchMock;
}

afterEach(() => {
    act(() => { store.dispatch(setSession('unknown')); });
    vi.unstubAllGlobals();
});

describe('App', () => {
    test('an existing session hides the login page and shows the nav', async () => {
        stubFetchWithSession(true);

        render(<App />, { wrapper: MemoryRouter });

        await waitFor(() => expect(document.querySelector('antrea-login-page')).toBeNull());
        expect(store.getState().session).toBe('authenticated');
    });

    test('an existing session shows the username and account kind in the header', async () => {
        stubFetchWithSession(true);

        render(<App />, { wrapper: MemoryRouter });

        await waitFor(() => {
            expect(document.querySelector('.app-user-identity-name')?.textContent).toBe('admin');
        });
        expect(document.querySelector('.app-user-identity-kind')?.textContent).toBe('Local Admin Account');
    });

    test('a Service Account session shows namespace:name, not the full system:serviceaccount: username', async () => {
        stubFetchWithSession(true, { mode: 'token', username: 'system:serviceaccount:antrea-ui:antrea-ui-admin' });

        render(<App />, { wrapper: MemoryRouter });

        await waitFor(() => {
            expect(document.querySelector('.app-user-identity-name')?.textContent).toBe('antrea-ui:antrea-ui-admin');
        });
        expect(document.querySelector('.app-user-identity-kind')?.textContent).toBe('Service Account');
    });

    test('no session keeps the login page up', async () => {
        stubFetchWithSession(false);

        render(<App />, { wrapper: MemoryRouter });

        await waitFor(() => expect(document.querySelector('antrea-login-page')).not.toBeNull());
        expect(store.getState().session).toBe('unknown');
    });

    test('logout: clicking Logout clears the session and shows the login page again', async () => {
        // useLogout() navigates via window.location.href — intercept the setter only, so jsdom
        // doesn't attempt a real navigation. That interception is also why /auth/session must
        // start returning 401 once it fires: unlike a real browser, jsdom does not unload the
        // page, so AuthShell mounts a fresh <antrea-login-page>, which re-probes the session —
        // and a real backend would already have cleared the cookie via GET /auth/logout by then.
        const hrefSetter = vi.fn();
        stubFetchWithSession(true);
        const fetchMock = fetch as Mock;
        fetchMock.mockImplementation(async (url: string) => {
            if (url === '/api/v1/settings') return jsonResponse(defaultSettings);
            if (url === '/auth/session') {
                return hrefSetter.mock.calls.length === 0
                    ? jsonResponse({ authenticated: true, mode: 'admin', username: 'admin' })
                    : new Response('not logged in', { status: 401 });
            }
            throw new Error(`unexpected fetch to ${url}`);
        });
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
            expect(store.getState().session).toBe('anonymous');
            expect(hrefSetter).toHaveBeenCalledTimes(1);
            expect(hrefSetter.mock.calls[0][0]).toContain('/auth/logout?');
        } finally {
            if (originalLocation) Object.defineProperty(window, 'location', originalLocation);
        }
    });

    // Without this, a tab left open past the 30-minute idle timeout logs the user out on their
    // next click — a password prompt for the admin mode, and a kubeconfig re-upload for mode 3.
    test('pings /auth/session on a timer while the tab is visible, and stops when it is hidden', async () => {
        // shouldAdvanceTime keeps waitFor()'s own polling working under fake timers.
        vi.useFakeTimers({ shouldAdvanceTime: true });
        try {
            const fetchMock = stubFetchWithSession(true);
            render(<App />, { wrapper: MemoryRouter });
            await waitFor(() => expect(document.querySelector('antrea-login-page')).toBeNull());

            const sessionCalls = () => fetchMock.mock.calls.filter(([url]) => url === '/auth/session').length;
            const initial = sessionCalls();

            await act(async () => { await vi.advanceTimersByTimeAsync(5 * 60 * 1000); });
            expect(sessionCalls()).toBe(initial + 1);

            // A hidden tab is genuinely idle and should be allowed to time out.
            const visibility = vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
            await act(async () => { await vi.advanceTimersByTimeAsync(15 * 60 * 1000); });
            expect(sessionCalls()).toBe(initial + 1);
            visibility.mockRestore();
        } finally {
            vi.useRealTimers();
        }
    });

    // A transient failure (a network blip, a 5xx, the backend mid-restart) must not be treated
    // like a 401: the session is very likely still fine, and the next tick will confirm it.
    test('a transient keepalive failure does not log the user out', async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        try {
            let sessionCalls = 0;
            const fetchMock = vi.fn(async (url: string) => {
                if (url === '/api/v1/settings') return jsonResponse(defaultSettings);
                if (url === '/auth/session') {
                    sessionCalls++;
                    if (sessionCalls === 1) return jsonResponse({ authenticated: true, mode: 'admin', username: 'admin' });
                    return new Response('internal error', { status: 500 });
                }
                throw new Error(`unexpected fetch to ${url}`);
            });
            vi.stubGlobal('fetch', fetchMock);

            render(<App />, { wrapper: MemoryRouter });
            await waitFor(() => expect(document.querySelector('antrea-login-page')).toBeNull());

            // 1 call before the timer even advances: the login page's own probe, already landed
            // by the time it disappears above. UserIdentity reads that same result from the
            // store rather than fetching its own.
            await act(async () => { await vi.advanceTimersByTimeAsync(5 * 60 * 1000); });
            expect(sessionCalls).toBe(2);
            expect(store.getState().session).toBe('authenticated');
        } finally {
            vi.useRealTimers();
        }
    });

    // A 401 on the keepalive ping means the session is genuinely over (idle timeout, absolute
    // cap, logout elsewhere, backend restart): this is the one case that must log the user out.
    test('a keepalive 401 logs the user out', async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        try {
            let sessionCalls = 0;
            const fetchMock = vi.fn(async (url: string) => {
                if (url === '/api/v1/settings') return jsonResponse(defaultSettings);
                if (url === '/auth/session') {
                    sessionCalls++;
                    if (sessionCalls === 1) return jsonResponse({ authenticated: true, mode: 'admin', username: 'admin' });
                    return new Response('session expired', { status: 401 });
                }
                throw new Error(`unexpected fetch to ${url}`);
            });
            vi.stubGlobal('fetch', fetchMock);

            render(<App />, { wrapper: MemoryRouter });
            await waitFor(() => expect(document.querySelector('antrea-login-page')).toBeNull());

            await act(async () => { await vi.advanceTimersByTimeAsync(5 * 60 * 1000); });
            expect(store.getState().session).toBe('anonymous');
        } finally {
            vi.useRealTimers();
        }
    });

    // Regression test: a keepalive 401 flips session to 'anonymous' while AuthShell stays
    // mounted, so React swaps in a brand new <antrea-login-page> rather than remounting AuthShell
    // itself. If the antrea-authenticated listener were attached with a plain ref (whose identity
    // never changes across renders) instead of a callback ref, it would still be listening to the
    // now-detached old element, and a subsequent successful re-login would dispatch into the
    // void: the cookie gets set, but the app never learns about it.
    test('re-login after a keepalive 401 is not lost', async () => {
        vi.useFakeTimers({ shouldAdvanceTime: true });
        try {
            let authenticated = true;
            const fetchMock = vi.fn(async (url: string) => {
                if (url === '/api/v1/settings') return jsonResponse(defaultSettings);
                if (url === '/auth/session') {
                    return authenticated
                        ? jsonResponse({ authenticated: true, mode: 'admin', username: 'admin' })
                        : new Response('not logged in', { status: 401 });
                }
                if (url === '/auth/login') {
                    authenticated = true;
                    return new Response('', { status: 200 });
                }
                throw new Error(`unexpected fetch to ${url}`);
            });
            vi.stubGlobal('fetch', fetchMock);

            render(<App />, { wrapper: MemoryRouter });
            await waitFor(() => expect(document.querySelector('antrea-login-page')).toBeNull());

            authenticated = false;
            await act(async () => { await vi.advanceTimersByTimeAsync(5 * 60 * 1000); });
            expect(store.getState().session).toBe('anonymous');
            await waitFor(() => expect(document.querySelector('antrea-login-page')).not.toBeNull());

            const loginPage = document.querySelector('antrea-login-page')!;
            await (loginPage as unknown as { updateComplete: Promise<unknown> }).updateComplete;
            const usernameEl = loginPage.shadowRoot!.querySelector<HTMLInputElement>('#username')!;
            const passwordEl = loginPage.shadowRoot!.querySelector<HTMLInputElement>('#password')!;
            usernameEl.value = 'admin';
            passwordEl.value = 'xyz';
            const form = loginPage.shadowRoot!.querySelector('form')!;
            await act(async () => {
                form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
                await new Promise(r => setTimeout(r, 0));
            });

            expect(store.getState().session).toBe('authenticated');
            await waitFor(() => expect(document.querySelector('.app-user-identity-name')?.textContent).toBe('admin'));
        } finally {
            vi.useRealTimers();
        }
    });
});
