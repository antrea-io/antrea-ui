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

import { afterEach, describe, expect, test, vi } from 'vitest';
import {
    apiLogin,
    apiLoginWithToken,
    apiLoginWithKubeconfig,
    apiSession,
    apiFetchAppSettings,
    sessionIdentity,
} from './auth-api';
import { APIError, setApiBase } from './api';

function jsonResponse(body: unknown, status = 200): Response {
    return new Response(JSON.stringify(body), { status });
}

afterEach(() => {
    vi.unstubAllGlobals();
    setApiBase('');
});

describe('apiLogin', () => {
    test('sends credentials as a Basic Authorization header and returns nothing', async () => {
        const fetchMock = vi.fn().mockResolvedValue(new Response('', { status: 200 }));
        vi.stubGlobal('fetch', fetchMock);

        // The response carries no token: on success the backend sets the session cookie.
        await expect(apiLogin('admin', 'xyz')).resolves.toBeUndefined();

        const [url, init] = fetchMock.mock.calls[0];
        expect(url).toBe('/auth/login');
        expect(init.method).toBe('POST');
        expect(init.headers.Authorization).toBe(`Basic ${btoa('admin:xyz')}`);
        expect(init.credentials).toBe('include');
    });

    test('throws APIError with the response body as the message on failure', async () => {
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('invalid password', {
            status: 401,
            statusText: 'Unauthorized',
        })));

        await expect(apiLogin('admin', 'wrong')).rejects.toMatchObject({
            code: 401,
            message: 'invalid password',
        });
    });
});

describe('apiLoginWithToken', () => {
    test('posts the token as JSON', async () => {
        const fetchMock = vi.fn().mockResolvedValue(new Response('', { status: 200 }));
        vi.stubGlobal('fetch', fetchMock);

        await apiLoginWithToken('sa-token');

        const [url, init] = fetchMock.mock.calls[0];
        expect(url).toBe('/auth/login/token');
        expect(init.method).toBe('POST');
        expect(JSON.parse(init.body)).toEqual({ token: 'sa-token' });
        expect(init.credentials).toBe('include');
    });

    test('surfaces the backend message when Kubernetes rejects the token', async () => {
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('Kubernetes rejected this credential', {
            status: 401,
            statusText: 'Unauthorized',
        })));

        await expect(apiLoginWithToken('bad')).rejects.toMatchObject({
            code: 401,
            message: 'Kubernetes rejected this credential',
        });
    });
});

describe('apiLoginWithKubeconfig', () => {
    test('posts the kubeconfig as JSON', async () => {
        const fetchMock = vi.fn().mockResolvedValue(new Response('', { status: 200 }));
        vi.stubGlobal('fetch', fetchMock);

        await apiLoginWithKubeconfig('apiVersion: v1\nkind: Config\n');

        const [url, init] = fetchMock.mock.calls[0];
        expect(url).toBe('/auth/login/kubeconfig');
        expect(JSON.parse(init.body)).toEqual({ kubeconfig: 'apiVersion: v1\nkind: Config\n' });
    });

    test('surfaces the explanation for an unsupported credential', async () => {
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(
            'kubeconfig uses an exec credential plugin, which Antrea UI cannot run on your behalf',
            { status: 400, statusText: 'Bad Request' },
        )));

        await expect(apiLoginWithKubeconfig('...')).rejects.toMatchObject({
            code: 400,
            message: expect.stringContaining('exec credential plugin'),
        });
    });
});

describe('apiSession', () => {
    test('returns the session info on success', async () => {
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({
            authenticated: true, mode: 'oidc', username: 'alice', expiresAt: '2026-08-02T12:00:00Z',
        })));

        const info = await apiSession();

        expect(info.authenticated).toBe(true);
        expect(info.mode).toBe('oidc');
        expect(info.username).toBe('alice');
    });

    test('throws APIError(401) when there is no session', async () => {
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('not logged in', {
            status: 401,
            statusText: 'Unauthorized',
        })));

        await expect(apiSession()).rejects.toBeInstanceOf(APIError);
    });

    test('sends the session cookie', async () => {
        const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ authenticated: true }));
        vi.stubGlobal('fetch', fetchMock);

        await apiSession();

        const [url, init] = fetchMock.mock.calls[0];
        expect(url).toBe('/auth/session');
        expect(init.credentials).toBe('include');
    });
});

describe('apiFetchAppSettings', () => {
    test('returns the parsed settings on success', async () => {
        const settings = {
            version: 'v1.0.0',
            auth: {
                basicEnabled: true,
                oidcEnabled: false,
                kubeconfigEnabled: true,
                serviceAccountTokenEnabled: true,
            },
        };
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(settings)));

        expect(await apiFetchAppSettings()).toEqual(settings);
    });

    test('throws APIError with a fallback message when the response body is empty', async () => {
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('', {
            status: 500,
            statusText: 'Internal Server Error',
        })));

        await expect(apiFetchAppSettings()).rejects.toMatchObject({
            code: 500,
            message: 'Failed to load app settings',
        });
    });

    test('does not send credentials — the settings endpoint does not read the session cookie', async () => {
        const fetchMock = vi.fn().mockResolvedValue(jsonResponse({}));
        vi.stubGlobal('fetch', fetchMock);

        await apiFetchAppSettings();

        const [, init] = fetchMock.mock.calls[0];
        expect(init?.credentials).toBeUndefined();
    });
});

describe('apiBase prefixing', () => {
    test('every auth call prepends the configured base URL', async () => {
        setApiBase('http://localhost:8080');
        const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(jsonResponse({})));
        vi.stubGlobal('fetch', fetchMock);

        await apiLogin('admin', 'xyz');
        expect(fetchMock.mock.calls[0][0]).toBe('http://localhost:8080/auth/login');

        await apiLoginWithToken('tok');
        expect(fetchMock.mock.calls[1][0]).toBe('http://localhost:8080/auth/login/token');

        await apiLoginWithKubeconfig('cfg');
        expect(fetchMock.mock.calls[2][0]).toBe('http://localhost:8080/auth/login/kubeconfig');

        await apiSession();
        expect(fetchMock.mock.calls[3][0]).toBe('http://localhost:8080/auth/session');

        await apiFetchAppSettings();
        expect(fetchMock.mock.calls[4][0]).toBe('http://localhost:8080/api/v1/settings');
    });
});

describe('sessionIdentity', () => {
    test('admin mode: the username is the local login name, regardless of what it looks like', () => {
        expect(sessionIdentity({ mode: 'admin', username: 'admin' }))
            .toEqual({ name: 'admin', kind: 'Local Admin Account' });
    });

    // The admin-password session impersonates the antrea-ui-admin ServiceAccount, but that is
    // not what this session's username is — admin mode must win even though the username here
    // matches the ServiceAccount pattern.
    test('admin mode wins over a ServiceAccount-shaped username', () => {
        expect(sessionIdentity({ mode: 'admin', username: 'system:serviceaccount:antrea-ui:antrea-ui-admin' }))
            .toEqual({ name: 'system:serviceaccount:antrea-ui:antrea-ui-admin', kind: 'Local Admin Account' });
    });

    test('a ServiceAccount username is trimmed to namespace:name, independent of login mode', () => {
        expect(sessionIdentity({ mode: 'token', username: 'system:serviceaccount:antrea-ui:antrea-ui-admin' }))
            .toEqual({ name: 'antrea-ui:antrea-ui-admin', kind: 'Service Account' });
    });

    // A kubeconfig can carry a ServiceAccount token just as easily as a pasted one — the
    // username, not the mode, is what says so.
    test('a ServiceAccount username reached via kubeconfig is still labeled Service Account', () => {
        expect(sessionIdentity({ mode: 'kubeconfig', username: 'system:serviceaccount:antrea-ui:reader' }))
            .toEqual({ name: 'antrea-ui:reader', kind: 'Service Account' });
    });

    test('a system: username that is not a ServiceAccount is a System User', () => {
        expect(sessionIdentity({ mode: 'kubeconfig', username: 'system:node:worker-1' }))
            .toEqual({ name: 'system:node:worker-1', kind: 'System User' });
    });

    test('an ordinary username is a User, whatever its login mode', () => {
        expect(sessionIdentity({ mode: 'oidc', username: 'alice@example.com' }))
            .toEqual({ name: 'alice@example.com', kind: 'User' });
        expect(sessionIdentity({ mode: 'kubeconfig', username: 'kubernetes-admin' }))
            .toEqual({ name: 'kubernetes-admin', kind: 'User' });
    });

    test('a malformed ServiceAccount-looking username (extra colons) falls through to System User, not split wrong', () => {
        expect(sessionIdentity({ mode: 'token', username: 'system:serviceaccount:a:b:c' }))
            .toEqual({ name: 'system:serviceaccount:a:b:c', kind: 'System User' });
    });

    test('mode is optional — a username alone is enough to derive an identity', () => {
        expect(sessionIdentity({ username: 'alice' })).toEqual({ name: 'alice', kind: 'User' });
    });
});
