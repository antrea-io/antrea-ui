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
import { apiFetch, apiFetchJSON, APIError, setApiBase, getApiBase } from './api';

function jsonResponse(body: unknown, status = 200): Response {
    return new Response(JSON.stringify(body), { status });
}

afterEach(() => {
    vi.unstubAllGlobals();
    setApiBase(''); // apiBase is module-level state — reset it so tests don't leak into each other
});

describe('setApiBase/getApiBase', () => {
    test('defaults to same-origin (empty string)', () => {
        expect(getApiBase()).toBe('');
    });

    test('apiFetch prepends the configured base to the request URL', async () => {
        const fetchMock = vi.fn().mockResolvedValue(new Response('ok', { status: 200 }));
        vi.stubGlobal('fetch', fetchMock);
        setApiBase('http://localhost:8080');

        await apiFetch('summary', 'my-token');

        expect(fetchMock.mock.calls[0][0]).toBe('http://localhost:8080/api/v1/summary');
    });
});

describe('APIError', () => {
    test('carries code, status and message', () => {
        const err = new APIError(404, 'Not Found', 'page not found');
        expect(err.code).toBe(404);
        expect(err.status).toBe('Not Found');
        expect(err.message).toBe('page not found');
        expect(err.name).toBe('APIError');
        expect(err).toBeInstanceOf(Error);
    });
});

describe('apiFetch', () => {
    test('sends a Bearer Authorization header when a token is provided', async () => {
        const fetchMock = vi.fn().mockResolvedValue(new Response('ok', { status: 200 }));
        vi.stubGlobal('fetch', fetchMock);

        await apiFetch('summary', 'my-token');

        expect(fetchMock).toHaveBeenCalledTimes(1);
        const [url, init] = fetchMock.mock.calls[0];
        expect(url).toBe('/api/v1/summary');
        expect((init.headers as Headers).get('Authorization')).toBe('Bearer my-token');
    });

    test('omits the Authorization header when the token is empty', async () => {
        const fetchMock = vi.fn().mockResolvedValue(new Response('ok', { status: 200 }));
        vi.stubGlobal('fetch', fetchMock);

        await apiFetch('summary', '');

        const [, init] = fetchMock.mock.calls[0];
        expect((init.headers as Headers).has('Authorization')).toBe(false);
    });

    test('preserves caller-supplied headers alongside Authorization', async () => {
        const fetchMock = vi.fn().mockResolvedValue(new Response('ok', { status: 200 }));
        vi.stubGlobal('fetch', fetchMock);

        await apiFetch('account/password', 'my-token', {
            method: 'PUT',
            headers: { 'content-type': 'application/json' },
        });

        const [, init] = fetchMock.mock.calls[0];
        expect(init.method).toBe('PUT');
        expect((init.headers as Headers).get('content-type')).toBe('application/json');
        expect((init.headers as Headers).get('Authorization')).toBe('Bearer my-token');
    });

    test('throws an APIError with the response body as the message on non-ok response', async () => {
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('permission denied', {
            status: 403,
            statusText: 'Forbidden',
        })));

        await expect(apiFetch('summary', 'my-token')).rejects.toMatchObject({
            code: 403,
            status: 'Forbidden',
            message: 'permission denied',
        });
    });

    test('falls back to "HTTP <status>" when the error response body is empty', async () => {
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('', {
            status: 401,
            statusText: 'Unauthorized',
        })));

        await expect(apiFetch('summary', 'my-token')).rejects.toMatchObject({
            code: 401,
            message: 'HTTP 401',
        });
    });

    test('normalizes a network-level fetch() rejection into an APIError', async () => {
        vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')));

        const err = await apiFetch('summary', 'my-token').catch(e => e);
        expect(err).toBeInstanceOf(APIError);
        expect(err.message).toBe('Failed to fetch');
    });
});

describe('apiFetchJSON', () => {
    test('parses the JSON response body', async () => {
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({ version: 'v1.0.0' })));

        const result = await apiFetchJSON<{ version: string }>('settings', 'my-token');

        expect(result).toEqual({ version: 'v1.0.0' });
    });

    test('propagates APIError from the underlying fetch on failure', async () => {
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('nope', { status: 500 })));

        await expect(apiFetchJSON('settings', 'my-token')).rejects.toBeInstanceOf(APIError);
    });
});
