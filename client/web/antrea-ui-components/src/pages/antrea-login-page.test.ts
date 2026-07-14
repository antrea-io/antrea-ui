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

import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';
import './antrea-login-page';
import type { AntreaLoginPage } from './antrea-login-page';

function jsonResponse(body: unknown, status = 200): Response {
    return new Response(JSON.stringify(body), { status });
}

function errorResponse(status: number, statusText: string, body = ''): Response {
    return new Response(body, { status, statusText });
}

interface MockFetchOptions {
    settings?: Response;
    refreshToken?: Response;
    login?: Response;
}

function mockFetch(opts: MockFetchOptions) {
    return vi.fn(async (url: string) => {
        if (url === '/api/v1/settings') return opts.settings ?? jsonResponse({});
        if (url === '/auth/refresh_token') return opts.refreshToken ?? errorResponse(401, 'Unauthorized');
        if (url === '/auth/login') return opts.login ?? errorResponse(401, 'Unauthorized');
        throw new Error(`unexpected fetch to ${url}`);
    });
}

const settingsBasicOnly = { version: 'v1.0.0', auth: { basicEnabled: true, oidcEnabled: false } };
const settingsOidcOnly = { version: 'v1.0.0', auth: { basicEnabled: false, oidcEnabled: true } };
const settingsBoth = { version: 'v1.0.0', auth: { basicEnabled: true, oidcEnabled: true } };
const settingsNone = { version: 'v1.0.0', auth: { basicEnabled: false, oidcEnabled: false } };
const settingsOidcNamed = {
    version: 'v1.0.0',
    auth: { basicEnabled: false, oidcEnabled: true, oidcProviderName: 'Dex' },
};

let el: AntreaLoginPage | undefined;

beforeEach(() => {
    localStorage.clear();
    window.history.pushState({}, '', '/');
});

afterEach(() => {
    el?.remove();
    el = undefined;
    vi.unstubAllGlobals();
});

async function mount(opts: MockFetchOptions): Promise<AntreaLoginPage> {
    vi.stubGlobal('fetch', mockFetch(opts));
    el = document.createElement('antrea-login-page') as AntreaLoginPage;
    document.body.appendChild(el);
    // Let the Promise.allSettled() in _init() and the resulting re-render flush.
    await el.updateComplete;
    await new Promise(r => setTimeout(r, 0));
    await el.updateComplete;
    return el;
}

describe('AntreaLoginPage — auth method visibility', () => {
    test('no auth methods enabled: no login form, no OIDC button', async () => {
        const page = await mount({ settings: jsonResponse(settingsNone) });
        expect(page.shadowRoot!.textContent).toContain('Please log in');
        expect(page.shadowRoot!.querySelector('form')).toBeNull();
        expect(page.shadowRoot!.querySelector('antrea-button[action="outline"]')).toBeNull();
    });

    test('basic only: shows the login form, no OIDC button', async () => {
        const page = await mount({ settings: jsonResponse(settingsBasicOnly) });
        expect(page.shadowRoot!.querySelector('form')).not.toBeNull();
        expect(page.shadowRoot!.querySelector('antrea-button[action="outline"]')).toBeNull();
    });

    test('oidc only: shows the OIDC button, no login form', async () => {
        const page = await mount({ settings: jsonResponse(settingsOidcOnly) });
        expect(page.shadowRoot!.querySelector('form')).toBeNull();
        const oidcButton = page.shadowRoot!.querySelector('antrea-button[action="outline"]');
        expect(oidcButton).not.toBeNull();
        expect(oidcButton!.textContent).toContain('Login with OIDC');
    });

    test('both enabled: shows both the login form and the OIDC button', async () => {
        const page = await mount({ settings: jsonResponse(settingsBoth) });
        expect(page.shadowRoot!.querySelector('form')).not.toBeNull();
        expect(page.shadowRoot!.querySelector('antrea-button[action="outline"]')).not.toBeNull();
    });

    test('OIDC button label uses the configured provider name', async () => {
        const page = await mount({ settings: jsonResponse(settingsOidcNamed) });
        const oidcButton = page.shadowRoot!.querySelector('antrea-button[action="outline"]');
        expect(oidcButton!.textContent).toContain('Login with Dex');
    });
});

describe('AntreaLoginPage — session refresh on connect', () => {
    test('refresh success: dispatches antrea-token and does not show the login form', async () => {
        // The element dispatches antrea-token from connectedCallback's async _init(), before
        // mount() returns — attaching a listener afterwards would miss it. Assert on the
        // rendered state instead, which stays stable once dispatched (see _tokenDispatched).
        const page = await mount({
            settings: jsonResponse(settingsBasicOnly),
            refreshToken: jsonResponse({ tokenType: 'Bearer', accessToken: 'existing-token', expiresIn: 3600 }),
        });
        expect(page.shadowRoot!.textContent).not.toContain('Please log in');
        expect(page.shadowRoot!.textContent).toContain('Authenticating');
    });

    test('refresh 401 (no session): shows the login form without an error banner', async () => {
        const page = await mount({
            settings: jsonResponse(settingsBasicOnly),
            refreshToken: errorResponse(401, 'Unauthorized', 'cookie expired'),
        });
        expect(page.shadowRoot!.textContent).toContain('Please log in');
        expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')).toBeNull();
    });

    test('refresh fails with a non-401 error: shows the login form with an error banner', async () => {
        const page = await mount({
            settings: jsonResponse(settingsBasicOnly),
            refreshToken: errorResponse(404, 'Not Found', 'not found'),
        });
        expect(page.shadowRoot!.textContent).toContain('Please log in');
        const alert = page.shadowRoot!.querySelector('antrea-alert[status="danger"]');
        expect(alert?.textContent).toContain('not found');
    });

    test('settings fetch fails: shows the settings error and no login form', async () => {
        const page = await mount({
            settings: errorResponse(500, 'Internal Server Error', 'settings unavailable'),
            refreshToken: errorResponse(401, 'Unauthorized'),
        });
        expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')?.textContent)
            .toContain('settings unavailable');
        expect(page.shadowRoot!.querySelector('form')).toBeNull();
        expect(page.shadowRoot!.textContent).not.toContain('Please log in');
    });
});

describe('AntreaLoginPage — basic login form', () => {
    async function submitLogin(page: AntreaLoginPage, username: string, password: string) {
        const usernameEl = page.shadowRoot!.querySelector<HTMLInputElement>('#username')!;
        const passwordEl = page.shadowRoot!.querySelector<HTMLInputElement>('#password')!;
        usernameEl.value = username;
        passwordEl.value = password;
        const form = page.shadowRoot!.querySelector('form')!;
        form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
        await page.updateComplete;
        await new Promise(r => setTimeout(r, 0));
        await page.updateComplete;
    }

    test('successful login dispatches antrea-token with the access token', async () => {
        const page = await mount({
            settings: jsonResponse(settingsBasicOnly),
            login: jsonResponse({ tokenType: 'Bearer', accessToken: 'new-token', expiresIn: 3600 }),
        });
        const onToken = vi.fn();
        page.addEventListener('antrea-token', onToken);

        await submitLogin(page, 'admin', 'xyz');

        expect(onToken).toHaveBeenCalledTimes(1);
        expect(onToken.mock.calls[0][0].detail).toEqual({ accessToken: 'new-token' });
    });

    test('failed login shows an error banner and does not dispatch antrea-token', async () => {
        const page = await mount({
            settings: jsonResponse(settingsBasicOnly),
            login: errorResponse(401, 'Unauthorized', 'invalid password'),
        });
        const onToken = vi.fn();
        page.addEventListener('antrea-token', onToken);

        await submitLogin(page, 'admin', 'wrong');

        expect(onToken).not.toHaveBeenCalled();
        expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')?.textContent)
            .toContain('invalid password');
    });
});

describe('AntreaLoginPage — success message banner', () => {
    test('shows a dismissible success banner from the ?msg= query param', async () => {
        window.history.pushState({}, '', '/?msg=logged%20out%20successfully');
        const page = await mount({ settings: jsonResponse(settingsBasicOnly) });

        const alert = page.shadowRoot!.querySelector('antrea-alert[status="success"]');
        expect(alert?.textContent).toContain('logged out successfully');

        alert!.dispatchEvent(new CustomEvent('antrea-close', { bubbles: true, composed: true }));
        await page.updateComplete;
        expect(page.shadowRoot!.querySelector('antrea-alert[status="success"]')).toBeNull();
    });
});

describe('AntreaLoginPage — OIDC auto-redirect', () => {
    let hrefSetter: ReturnType<typeof vi.fn>;
    let originalLocation: PropertyDescriptor | undefined;

    beforeEach(() => {
        // Navigate for real first, so window.location stays same-origin/consistent for
        // history.replaceState() (called by _readUrlParams() to strip ?auth_method= from the
        // URL) — then wrap the real Location in a Proxy that only intercepts the `href` setter,
        // so we can observe the OIDC redirect without jsdom attempting a real navigation.
        window.history.pushState({}, '', '/?auth_method=oidc');
        hrefSetter = vi.fn();
        originalLocation = Object.getOwnPropertyDescriptor(window, 'location');
        const realLocation = window.location;
        const proxiedLocation = new Proxy(realLocation, {
            set(target, prop, value) {
                if (prop === 'href') { hrefSetter(value); return true; }
                return Reflect.set(target, prop, value);
            },
        });
        Object.defineProperty(window, 'location', { value: proxiedLocation, configurable: true });
    });

    afterEach(() => {
        if (originalLocation) Object.defineProperty(window, 'location', originalLocation);
    });

    test('?auth_method=oidc followed by a 401 refresh auto-triggers the OIDC redirect', async () => {
        await mount({
            settings: jsonResponse(settingsOidcOnly),
            refreshToken: errorResponse(401, 'Unauthorized'),
        });

        expect(localStorage.getItem('ui.antrea.io/use-oidc')).toBeNull();
        expect(hrefSetter).toHaveBeenCalledTimes(1);
        const redirectUrl = hrefSetter.mock.calls[0][0] as string;
        expect(redirectUrl).toContain('/auth/oauth2/login?');
        // _readUrlParams() strips ?auth_method= from the URL before _doOidcLogin() runs, so the
        // captured redirect_url reflects the cleaned-up location (no auth_method param), not
        // the original one.
        const params = new URLSearchParams(redirectUrl.split('?')[1]);
        expect(params.get('redirect_url')).not.toContain('auth_method');
    });
});
