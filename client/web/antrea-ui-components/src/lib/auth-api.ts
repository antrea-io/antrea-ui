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

/** How the user authenticated. Mirrors the backend's session modes. */
export type SessionMode = 'oidc' | 'kubeconfig' | 'admin' | 'serviceAccountToken';

/** Describes the caller's session. Never contains any credential material: the Kubernetes
 * credential lives only in the backend's memory, and the browser only holds an opaque cookie. */
export interface SessionInfo {
    authenticated: boolean
    mode?: SessionMode
    /** Display only. Authorization is always Kubernetes' decision. */
    username?: string
    /** RFC 3339 timestamp of the latest the session can possibly last: the backend's absolute
     * lifetime cap, or the expiry of the credential behind it if that comes first. Activity does
     * not extend it, and an idle session ends well before it. */
    expiresAt?: string
}

export interface AppSettings {
    version: string
    auth: {
        /** Static admin password. */
        basicEnabled: boolean
        oidcEnabled: boolean
        oidcProviderName?: string
        /** Upload your own kubeconfig. */
        kubeconfigEnabled: boolean
        /** Paste a Kubernetes token. */
        serviceAccountTokenEnabled: boolean
    }
    features?: {
        flowVisibilityEnabled?: boolean
    }
}

async function throwIfNotOk(res: Response, fallback: string): Promise<Response> {
    if (res.ok) return res;
    let msg = fallback;
    try { const t = await res.text(); if (t) msg = t; } catch { /* ignore */ }
    throw new APIError(res.status, res.statusText, msg);
}

async function authFetch(path: string, options: RequestInit = {}): Promise<Response> {
    const res = await fetch(`${getApiBase()}${path}`, { credentials: 'include', ...options });
    return throwIfNotOk(res, `HTTP ${res.status}`);
}

async function postJSON(path: string, body: unknown): Promise<void> {
    await authFetch(path, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });
}

/**
 * Logs in with the static admin password. On success the backend sets the session cookie; there
 * is no token in the response for the client to hold onto.
 */
export async function apiLogin(username: string, password: string): Promise<void> {
    await authFetch('/auth/login', {
        method: 'POST',
        headers: { 'Authorization': `Basic ${btoa(`${username}:${password}`)}` },
    });
}

/** Logs in with a pasted Kubernetes bearer token (typically a ServiceAccount token). */
export async function apiLoginWithToken(token: string): Promise<void> {
    return postJSON('/auth/login/token', { token });
}

/**
 * Logs in with a kubeconfig. The backend extracts the current context's credential and discards
 * the rest immediately; `exec` and `auth-provider` credentials are rejected, since they describe
 * something to run on your machine.
 */
export async function apiLoginWithKubeconfig(kubeconfig: string): Promise<void> {
    return postJSON('/auth/login/kubeconfig', { kubeconfig });
}

/**
 * Returns the caller's session, and throws an APIError with code 401 if there is none.
 *
 * This is both the app-start "am I logged in?" probe and the idle keepalive: every call bumps the
 * session's last-seen time on the backend.
 */
export async function apiSession(): Promise<SessionInfo> {
    const res = await authFetch('/auth/session');
    return res.json();
}

export async function apiFetchAppSettings(): Promise<AppSettings> {
    // Deliberately a bare fetch(), not authFetch(): the settings endpoint doesn't read the
    // session cookie, so it shouldn't send credentials cross-origin (that'd newly require the
    // server to send Access-Control-Allow-Credentials for no reason).
    const res = await fetch(`${getApiBase()}/api/v1/settings`);
    await throwIfNotOk(res, 'Failed to load app settings');
    return res.json();
}
