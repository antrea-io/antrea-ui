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

/** How the user authenticated: the mechanism the credential reached us through — not what kind
 * of account it belongs to. A kubeconfig can carry a ServiceAccount token, and a pasted bearer
 * token need not be one; see sessionIdentity(), which derives the account kind from `username`
 * instead. Mirrors the backend's session modes. */
export type SessionMode = 'oidc' | 'kubeconfig' | 'admin' | 'token';

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

/** How to present a session in the UI: the identity Kubernetes knows, plus what kind of account
 * it is. */
export interface SessionIdentity {
    name: string
    kind: string
}

// A ServiceAccount's Kubernetes username: "system:serviceaccount:<namespace>:<name>". Only the
// namespace/name pair is useful for display.
const SERVICE_ACCOUNT_USERNAME = /^system:serviceaccount:([^:]+:[^:]+)$/;

/** Derived from `username`, which is what the API server actually authenticated as — not from
 * `mode`, which only says how the credential reached us. Display only; authorization is always
 * the API server's decision. */
export function sessionIdentity({ mode, username }: { mode?: SessionMode, username: string }): SessionIdentity {
    // The static admin password is not a Kubernetes identity: the session impersonates the
    // antrea-ui-admin ServiceAccount, and `username` is the local login name, not that
    // ServiceAccount's.
    if (mode === 'admin') return { name: username, kind: 'Local Admin Account' };
    const serviceAccount = username.match(SERVICE_ACCOUNT_USERNAME)?.[1];
    if (serviceAccount) return { name: serviceAccount, kind: 'Service Account' };
    // Kubernetes reserves the "system:" prefix for the control plane's own identities, so a
    // login as one (a kubelet's kubeconfig, say) is an infrastructure identity, not a person.
    // Must come after the ServiceAccount case, which is itself a "system:" name.
    if (username.startsWith('system:')) return { name: username, kind: 'System User' };
    return { name: username, kind: 'User' };
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
