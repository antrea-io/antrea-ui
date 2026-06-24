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

    const response = await fetch(`/api/v1/${path}`, {
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
