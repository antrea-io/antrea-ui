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

export class APIError extends Error {
    code: number;
    status: string;

    constructor(code: number, status: string, message: string) {
        super(message);
        this.name = 'APIError';
        this.code = code;
        this.status = status;
    }
}

// Origin prepended to every request this library makes (api.ts and auth-api.ts), so a host
// whose frontend and backend run on different origins (e.g. local dev: antrea-ui's Vite dev
// server on :3000, the Go backend on :8080, no dev proxy configured) can point requests at the
// right place. Defaults to '' (same-origin relative requests), which is correct for the normal
// deployed case (nginx serves both from one origin). Deliberately a runtime setter rather than
// this library reading a bundler env var itself: an env var like Vite's import.meta.env.* would
// get inlined at *this* library's own build time, not the host's — the wrong layer, since
// antrea-ui-components is built and published independently of any one host.
let apiBase = '';

/** Host apps call this once at startup if their frontend and backend are on different origins. */
export function setApiBase(base: string): void {
    apiBase = base;
}

export function getApiBase(): string {
    return apiBase;
}

/**
 * Authenticated fetch wrapper. All page components use this instead of axios.
 *
 * On 401 the caller receives an APIError with code 401 — the page component
 * should dispatch 'antrea-session-expired' so the host can refresh the token
 * and re-set the token property.
 */
export async function apiFetch(
    path: string,
    token: string,
    options: RequestInit = {},
): Promise<Response> {
    const headers = new Headers(options.headers as HeadersInit | undefined);
    if (token) headers.set('Authorization', `Bearer ${token}`);

    const response = await fetch(`${apiBase}/api/v1/${path}`, {
        ...options,
        headers,
    });

    if (!response.ok) {
        let message = `HTTP ${response.status}`;
        try {
            const body = await response.text();
            if (body) message = body;
        } catch { /* ignore */ }
        throw new APIError(response.status, response.statusText, message);
    }
    return response;
}

export async function apiFetchJSON<T>(
    path: string,
    token: string,
    options: RequestInit = {},
): Promise<T> {
    const res = await apiFetch(path, token, options);
    return res.json() as Promise<T>;
}
