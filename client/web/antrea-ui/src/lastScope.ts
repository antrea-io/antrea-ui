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

// The last scope chosen: the last valid Namespace scope a Namespace-aware page took from its
// picker or its URL. One for the whole UI, kept for the browser tab (sessionStorage), so a reload
// keeps it, a new tab starts without one, and signing out and back in as the same user keeps it.
// It belongs to the signed-in user and is never offered to another one on the same tab.
//
// A scope is how it appears in a URL: a Namespace name, or `_all` for All Namespaces.

const KEY = 'antrea-ui:last-scope';

interface Stored {
    user: string
    scope: string
}

export function setLastScope(user: string, scope: string): void {
    try {
        sessionStorage.setItem(KEY, JSON.stringify({ user, scope } satisfies Stored));
    } catch {
        // Storage can be unavailable or full. The scope is a convenience, so losing it is fine.
    }
}

/** The last scope `user` chose in this tab, or null when there is none. */
export function getLastScope(user: string): string | null {
    try {
        const raw = sessionStorage.getItem(KEY);
        if (!raw) return null;
        const stored = JSON.parse(raw) as Partial<Stored>;
        return stored.user === user && typeof stored.scope === 'string' ? stored.scope : null;
    } catch {
        return null;
    }
}

/** The query string that carries `scope` to a Namespace-aware page, or '' for none. */
export function scopeSearch(scope: string | null): string {
    return scope ? `?${new URLSearchParams({ namespace: scope })}` : '';
}
