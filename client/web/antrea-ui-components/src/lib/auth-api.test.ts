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
import { apiLogin, apiRefreshToken, apiFetchAppSettings } from './auth-api';
import { APIError } from './api';

function jsonResponse(body: unknown, status = 200): Response {
    return new Response(JSON.stringify(body), { status });
}

afterEach(() => {
    vi.unstubAllGlobals();
});

describe('apiLogin', () => {
    test('sends credentials as a Basic Authorization header', async () => {
        const fetchMock = vi.fn().mockResolvedValue(jsonResponse({
            tokenType: 'Bearer', accessToken: 'my-token', expiresIn: 3600,
        }));
        vi.stubGlobal('fetch', fetchMock);

        const token = await apiLogin('admin', 'xyz');

        expect(token).toEqual({ tokenType: 'Bearer', accessToken: 'my-token', expiresIn: 3600 });
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

describe('apiRefreshToken', () => {
    test('returns the refreshed token on success', async () => {
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({
            tokenType: 'Bearer', accessToken: 'refreshed-token', expiresIn: 3600,
        })));

        const token = await apiRefreshToken();

        expect(token.accessToken).toBe('refreshed-token');
    });

    test('throws APIError(401) when there is no valid session cookie', async () => {
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('cookie expired', {
            status: 401,
            statusText: 'Unauthorized',
        })));

        await expect(apiRefreshToken()).rejects.toBeInstanceOf(APIError);
    });
});

describe('apiFetchAppSettings', () => {
    test('returns the parsed settings on success', async () => {
        const settings = { version: 'v1.0.0', auth: { basicEnabled: true, oidcEnabled: false } };
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
});
