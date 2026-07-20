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

import { APIError, getApiBase } from './api.js';

export interface Token {
    tokenType: string
    accessToken: string
    expiresIn: number
}

export interface AppSettings {
    version: string
    auth: {
        basicEnabled: boolean
        oidcEnabled: boolean
        oidcProviderName?: string
    }
    features?: {
        flowVisibilityEnabled?: boolean
    }
}

async function unauthFetch(url: string, options: RequestInit = {}): Promise<Response> {
    const res = await fetch(url, { credentials: 'include', ...options });
    if (!res.ok) {
        let msg = `HTTP ${res.status}`;
        try { const t = await res.text(); if (t) msg = t; } catch { /* ignore */ }
        throw new APIError(res.status, res.statusText, msg);
    }
    return res;
}

export async function apiLogin(username: string, password: string): Promise<Token> {
    const res = await unauthFetch(`${getApiBase()}/auth/login`, {
        method: 'POST',
        headers: { 'Authorization': `Basic ${btoa(`${username}:${password}`)}` },
    });
    return res.json();
}

export async function apiRefreshToken(): Promise<Token> {
    const res = await unauthFetch(`${getApiBase()}/auth/refresh_token`);
    return res.json();
}

export async function apiFetchAppSettings(): Promise<AppSettings> {
    const res = await fetch(`${getApiBase()}/api/v1/settings`);
    if (!res.ok) {
        let msg = 'Failed to load app settings';
        try { const t = await res.text(); if (t) msg = t; } catch { /* ignore */ }
        throw new APIError(res.status, res.statusText, msg);
    }
    return res.json();
}
