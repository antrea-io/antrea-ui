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

import { afterEach, beforeEach, describe, expect, test, vi, type Mock } from 'vitest';
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
    session?: Response;
    login?: Response;
    loginToken?: Response;
    loginKubeconfig?: Response;
}

function mockFetch(opts: MockFetchOptions) {
    return vi.fn(async (url: string) => {
        if (url === '/api/v1/settings') return opts.settings ?? jsonResponse({});
        if (url === '/auth/session') return opts.session ?? errorResponse(401, 'Unauthorized');
        if (url === '/auth/login') return opts.login ?? errorResponse(401, 'Unauthorized');
        if (url === '/auth/login/token') return opts.loginToken ?? errorResponse(401, 'Unauthorized');
        if (url === '/auth/login/kubeconfig') return opts.loginKubeconfig ?? errorResponse(400, 'Bad Request');
        throw new Error(`unexpected fetch to ${url}`);
    });
}

function authSettings(overrides: Record<string, unknown>) {
    return {
        version: 'v1.0.0',
        auth: {
            basicEnabled: false,
            oidcEnabled: false,
            kubeconfigEnabled: false,
            tokenEnabled: false,
            ...overrides,
        },
    };
}

const settingsBasicOnly = authSettings({ basicEnabled: true });
const settingsOidcOnly = authSettings({ oidcEnabled: true });
const settingsBoth = authSettings({ basicEnabled: true, oidcEnabled: true });
const settingsNone = authSettings({});
const settingsOidcNamed = authSettings({ oidcEnabled: true, oidcProviderName: 'Dex' });
const settingsTokenOnly = authSettings({ tokenEnabled: true });
const settingsKubeconfigOnly = authSettings({ kubeconfigEnabled: true });

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

describe('AntreaLoginPage — session probe on connect', () => {
    test('existing session: dispatches antrea-authenticated and does not show the login form', async () => {
        // The element dispatches antrea-authenticated from connectedCallback's async _init(),
        // before mount() returns — attaching a listener afterwards would miss it. Assert on the
        // rendered state instead, which stays stable once dispatched.
        const page = await mount({
            settings: jsonResponse(settingsBasicOnly),
            session: jsonResponse({ authenticated: true, mode: 'admin', username: 'admin' }),
        });
        expect(page.shadowRoot!.textContent).not.toContain('Please log in');
        expect(page.shadowRoot!.textContent).toContain('Authenticating');
    });

    test('existing session: the event detail carries the session info from the same probe, not a second fetch', async () => {
        vi.stubGlobal('fetch', mockFetch({
            settings: jsonResponse(settingsBasicOnly),
            session: jsonResponse({ authenticated: true, mode: 'admin', username: 'admin' }),
        }));
        el = document.createElement('antrea-login-page') as AntreaLoginPage;
        const onAuth = vi.fn();
        // Attached before the element joins the DOM: connectedCallback's _init() dispatches
        // synchronously with respect to its own microtasks, so a listener added after
        // appendChild (as the shared mount() helper does) would miss it.
        el.addEventListener('antrea-authenticated', onAuth);
        document.body.appendChild(el);
        await el.updateComplete;
        await new Promise(r => setTimeout(r, 0));

        expect(onAuth).toHaveBeenCalledTimes(1);
        expect((onAuth.mock.calls[0][0] as CustomEvent).detail).toEqual({ authenticated: true, mode: 'admin', username: 'admin' });
        expect((fetch as Mock).mock.calls.filter(([url]) => url === '/auth/session')).toHaveLength(1);
    });

    test('401 (no session): shows the login form without an error banner', async () => {
        const page = await mount({
            settings: jsonResponse(settingsBasicOnly),
            session: errorResponse(401, 'Unauthorized', 'not logged in'),
        });
        expect(page.shadowRoot!.textContent).toContain('Please log in');
        expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')).toBeNull();
    });

    test('probe fails with a non-401 error: shows the login form with an error banner', async () => {
        const page = await mount({
            settings: jsonResponse(settingsBasicOnly),
            session: errorResponse(404, 'Not Found', 'not found'),
        });
        expect(page.shadowRoot!.textContent).toContain('Please log in');
        const alert = page.shadowRoot!.querySelector('antrea-alert[status="danger"]');
        expect(alert?.textContent).toContain('not found');
    });

    test('settings fetch fails: shows the settings error and no login form', async () => {
        const page = await mount({
            settings: errorResponse(500, 'Internal Server Error', 'settings unavailable'),
            session: errorResponse(401, 'Unauthorized'),
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

    // No token crosses this boundary any more: the backend set an HttpOnly cookie. The login
    // response itself carries no session info, so a successful login fetches it with one more
    // GET /auth/session before telling the host — that's the same info the host would otherwise
    // have to fetch itself just to display who is logged in.
    test('successful login fetches the session info and dispatches it as the event payload', async () => {
        // The initial connect-time probe must still see "no session" (401) so the login form
        // renders at all; only the post-submit probe should find the newly-created session.
        let sessionCalls = 0;
        const fetchMock = vi.fn(async (url: string) => {
            if (url === '/api/v1/settings') return jsonResponse(settingsBasicOnly);
            if (url === '/auth/session') {
                sessionCalls++;
                return sessionCalls === 1
                    ? errorResponse(401, 'Unauthorized')
                    : jsonResponse({ authenticated: true, mode: 'admin', username: 'admin' });
            }
            if (url === '/auth/login') return new Response('', { status: 200 });
            throw new Error(`unexpected fetch to ${url}`);
        });
        vi.stubGlobal('fetch', fetchMock);
        el = document.createElement('antrea-login-page') as AntreaLoginPage;
        document.body.appendChild(el);
        await el.updateComplete;
        await new Promise(r => setTimeout(r, 0));
        await el.updateComplete;

        const onAuth = vi.fn();
        el.addEventListener('antrea-authenticated', onAuth);
        await submitLogin(el, 'admin', 'xyz');

        expect(onAuth).toHaveBeenCalledTimes(1);
        expect((onAuth.mock.calls[0][0] as CustomEvent).detail).toEqual({ authenticated: true, mode: 'admin', username: 'admin' });
    });

    // The login itself already succeeded (the cookie is set) — losing this best-effort follow-up
    // fetch must not turn into losing the login. The host just gets no identity to display.
    test('a successful login still dispatches antrea-authenticated even if the follow-up session fetch fails', async () => {
        const page = await mount({
            settings: jsonResponse(settingsBasicOnly),
            login: new Response('', { status: 200 }),
            session: errorResponse(401, 'Unauthorized'),
        });
        const onAuth = vi.fn();
        page.addEventListener('antrea-authenticated', onAuth);

        await submitLogin(page, 'admin', 'xyz');

        expect(onAuth).toHaveBeenCalledTimes(1);
        expect((onAuth.mock.calls[0][0] as CustomEvent).detail).toBeNull();
    });

    test('failed login shows an error banner and does not dispatch antrea-authenticated', async () => {
        const page = await mount({
            settings: jsonResponse(settingsBasicOnly),
            login: errorResponse(401, 'Unauthorized', 'invalid password'),
        });
        const onToken = vi.fn();
        page.addEventListener('antrea-authenticated', onToken);

        await submitLogin(page, 'admin', 'wrong');

        expect(onToken).not.toHaveBeenCalled();
        expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')?.textContent)
            .toContain('invalid password');
    });

    test('an empty username or password is rejected client-side, without calling the API', async () => {
        const page = await mount({ settings: jsonResponse(settingsBasicOnly) });
        const fetchMock = fetch as Mock;
        fetchMock.mockClear();

        await submitLogin(page, '', 'xyz');
        expect(fetchMock).not.toHaveBeenCalledWith('/auth/login', expect.anything());
        expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')?.textContent)
            .toContain('required');

        await submitLogin(page, 'admin', '');
        expect(fetchMock).not.toHaveBeenCalledWith('/auth/login', expect.anything());
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
    let hrefSetter: Mock;
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

    test('?auth_method=oidc with no session auto-triggers the OIDC redirect', async () => {
        await mount({
            settings: jsonResponse(settingsOidcOnly),
            session: errorResponse(401, 'Unauthorized'),
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

describe('AntreaLoginPage — Kubernetes credential login', () => {
    async function submitFirstForm(page: AntreaLoginPage) {
        page.shadowRoot!.querySelector('form')!
            .dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
        await page.updateComplete;
        await new Promise(r => setTimeout(r, 0));
        await page.updateComplete;
    }

    test('the token control is only rendered when that mode is enabled', async () => {
        const page = await mount({ settings: jsonResponse(settingsBasicOnly) });
        expect(page.shadowRoot!.querySelector('#sa-token')).toBeNull();
        page.remove();

        el = await mount({ settings: jsonResponse(settingsTokenOnly) });
        expect(el.shadowRoot!.querySelector('#sa-token')).not.toBeNull();
    });

    test('the kubeconfig control is only rendered when that mode is enabled', async () => {
        const page = await mount({ settings: jsonResponse(settingsBasicOnly) });
        expect(page.shadowRoot!.querySelector('#kubeconfig')).toBeNull();
        page.remove();

        el = await mount({ settings: jsonResponse(settingsKubeconfigOnly) });
        expect(el.shadowRoot!.querySelector('#kubeconfig')).not.toBeNull();
    });

    test('submitting a token posts it and dispatches antrea-authenticated', async () => {
        const page = await mount({
            settings: jsonResponse(settingsTokenOnly),
            loginToken: new Response('', { status: 200 }),
        });
        const onAuth = vi.fn();
        page.addEventListener('antrea-authenticated', onAuth);

        page.shadowRoot!.querySelector<HTMLTextAreaElement>('#sa-token')!.value = '  my-sa-token  ';
        await submitFirstForm(page);

        const call = (fetch as Mock).mock.calls.find(([url]) => url === '/auth/login/token');
        expect(call).toBeDefined();
        // Pasted tokens often carry stray whitespace, so they are trimmed before being sent.
        expect(JSON.parse(call![1].body)).toEqual({ token: 'my-sa-token' });
        expect(onAuth).toHaveBeenCalledTimes(1);
    });

    test('a rejected kubeconfig surfaces the backend explanation', async () => {
        const page = await mount({
            settings: jsonResponse(settingsKubeconfigOnly),
            loginKubeconfig: errorResponse(400, 'Bad Request',
                'kubeconfig uses an exec credential plugin, which Antrea UI cannot run on your behalf'),
        });

        page.shadowRoot!.querySelector<HTMLTextAreaElement>('#kubeconfig')!.value = 'apiVersion: v1';
        await submitFirstForm(page);

        expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')?.textContent)
            .toContain('exec credential plugin');
    });

    test('an empty token is rejected client-side, without calling the API', async () => {
        const page = await mount({ settings: jsonResponse(settingsTokenOnly) });
        const fetchMock = fetch as Mock;
        fetchMock.mockClear();

        await submitFirstForm(page);

        expect(fetchMock).not.toHaveBeenCalledWith('/auth/login/token', expect.anything());
        expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')?.textContent)
            .toContain('required');
    });
});
