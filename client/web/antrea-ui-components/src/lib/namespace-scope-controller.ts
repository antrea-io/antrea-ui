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

import { accessSummary, pageVerdict } from './access-api.js';
import type { AccessSummary, ResourceQuery, Verdict } from './access-api.js';
import { APIError } from './api.js';
import { isValidNamespaceName, namespaceAccess, scopeNote } from './namespace-access.js';
import type { NamespaceAccess, ScopeNote } from './namespace-access.js';
import {
    SCOPE_ALL, parseScopeParam, dispatchScopeChosen, writeScopeToURL,
    scopeToValue, valueToScope,
} from './namespace-scope.js';
import type { NamespaceScope } from './namespace-scope.js';

// Shared Namespace selection and URL handling for Overview and Flow Visibility.

/** What a page knows about its scope. A new object on every change. */
export interface NamespaceScopeState {
    /** The scope in force, or null for none (still loading, an invalid URL, or nothing to pick). */
    scope: NamespaceScope | null;
    /** Where it came from. Only 'url' and 'picker' are choices: a 'default' is never reported to
     * the host or written to the URL. */
    source: 'url' | 'picker' | 'default' | null;
    /** What the picker offers. Null until the access summary and the Namespace list have loaded
     * or failed. */
    access: NamespaceAccess | null;
    /** Why there is no scope or the requested one was refused: an invalid `?namespace=`, or a
     * typed name that is not a Namespace name. */
    error: string | null;
    /** The note on a selected Namespace that is not in the list. */
    note: ScopeNote | null;
    /** Whether All Namespaces is offered: the verdict is not denied, or it is in force. */
    offerAll: boolean;
}

export interface NamespaceScopeOptions {
    /** Where the scope events are dispatched from: the page's own element, or any node inside it
     * (the events bubble and are composed). */
    target: EventTarget;
    /** The page's permissions, any-of. They decide the cluster-wide
     * verdict. Absent: allowed for a cluster admin, unknown for anyone else. */
    resources?: ResourceQuery[];
    /** Called instead of dispatching `antrea-session-expired` from `target`, for a page that has
     * more to do when the session dies. */
    onSessionExpired?: () => void;
    /** Whether the page is worth loading for right now. A load that finishes while it says no is
     * dropped, and refreshAccess() runs it again. Absent: always. */
    isActive?: () => boolean;
}

export interface NamespaceScopeHandle {
    state(): NamespaceScopeState;
    /** Calls `listener` with the new state on every change. Returns an unsubscribe function. */
    subscribe(listener: (state: NamespaceScopeState) => void): () => void;
    /** Feeds it the location's query string, on the first call and each time it changes.
     * undefined: read window.location.search, and follow popstate. */
    setLocationSearch(search: string | undefined): void;
    /** The user chose a scope. `typed`: a typed value is checked as a Namespace name. Writes the
     * URL and reports the choice. */
    select(scope: NamespaceScope, typed?: boolean): void;
    /** Replaces the permissions the cluster-wide verdict is about. */
    setResources(resources: ResourceQuery[] | undefined): void;
    /** Loads what the picker offers again, if it has not loaded. */
    refreshAccess(): void;
    /** Stops listening and drops pending work. Call it when the page goes away. */
    dispose(): void;
}

// A query string without regard to the order of its parameters: a host may hand back what the page
// wrote with the parameters in another order.
function canonicalSearch(search: string): string {
    const params = new URLSearchParams(search);
    params.sort();
    return params.toString();
}

function isUnauthorized(err: unknown): boolean {
    return err instanceof APIError && err.code === 401;
}

// Equivalent permissions from a fresh render must not clear the scope or restart its requests.
function resourcesKey(resources: ResourceQuery[] | undefined): string | undefined {
    return resources === undefined ? undefined : JSON.stringify(resources
        .map(q => JSON.stringify([q.group, q.resource, q.verb, q.name])).sort());
}

/** Creates the scope controller of one page. */
export function createNamespaceScope(options: NamespaceScopeOptions): NamespaceScopeHandle {
    const { target } = options;
    let resources = options.resources;
    let resourceKey = resourcesKey(resources);
    let disposed = false;
    let started = false;
    let followPopState = false;
    // The query string of the URL as the page last took or wrote it, so that a host handing back
    // what the page wrote is not mistaken for a navigation.
    let appliedSearch = '';
    let accessGeneration = 0;
    let accessLoading = false;
    let noteToken = 0;
    // The scope's picker value ('' for none), and the rest of the state.
    let value = '';
    let source: NamespaceScopeState['source'] = null;
    let access: NamespaceAccess | null = null;
    let error: string | null = null;
    let note: ScopeNote | null = null;
    let current = snapshot();
    const listeners = new Set<(state: NamespaceScopeState) => void>();

    function snapshot(): NamespaceScopeState {
        const scope = valueToScope(value);
        return {
            scope, source, access, error, note,
            offerAll: (access?.clusterWide ?? 'unknown') !== 'denied' || scope?.kind === 'all',
        };
    }

    function notify() {
        current = snapshot();
        for (const listener of [...listeners]) {
            try {
                listener(current);
            } catch (e) {
                console.error('namespace scope listener failed', e);
            }
        }
    }

    function clusterWide(s: AccessSummary | null): Verdict {
        if (resources) return pageVerdict(s, resources);
        return s?.clusterAdmin ? 'allowed' : 'unknown';
    }

    function expireSession() {
        if (options.onSessionExpired) options.onSessionExpired();
        else target.dispatchEvent(new CustomEvent('antrea-session-expired', { bubbles: true, composed: true }));
    }

    // The scope when the URL names none: All Namespaces where the page's cluster-wide verdict is
    // allowed, otherwise the first Namespace of the list, otherwise nothing. An unknown verdict
    // never makes All Namespaces the default. A default spares the user a page that shows nothing
    // until they open the picker. All Namespaces wins because defaulting a user who can see the
    // whole cluster into its alphabetically first Namespace would look like the page had lost most
    // of its data; on Flow Visibility it is also the only scope in which nothing is redacted. The
    // default waits for the list and the verdict, and is neither written to the URL nor reported
    // to the host: it is not a choice, and a reload decides it again from the current permissions
    // and list.
    function setDefault() {
        value = access?.clusterWide === 'allowed' ? SCOPE_ALL : (access?.candidates?.[0] ?? '');
        source = value ? 'default' : null;
    }

    function publishScope() {
        note = null;
        notify();
        void refreshNote();
    }

    function publishAccess() {
        // A permissions update may change a default. Explicit choices keep their source.
        if (parseScopeParam(appliedSearch).kind === 'none') setDefault();
        publishScope();
    }

    /** Decides the scope from the URL, or else from the default order. A scope in the URL is
     * used as given, with no fallback; an invalid one is reported and nothing is selected. */
    function applyFromURL(search: string) {
        const parsed = parseScopeParam(search);
        if (parsed.kind === 'invalid') {
            error = parsed.message;
            value = '';
            source = null;
        } else if (parsed.kind === 'scope') {
            error = null;
            value = parsed.value;
            source = 'url';
            dispatchScopeChosen(target, parsed.value);
        } else {
            error = null;
            setDefault();
        }
        publishScope();
    }

    async function refreshNote() {
        const token = ++noteToken;
        const ns = value === SCOPE_ALL ? '' : value;
        const isCurrent = () => token === noteToken && !disposed;
        const result = await scopeNote(access, ns, isCurrent, expireSession);
        if (!isCurrent()) return;
        // Not worth a notification when there is none to show and none was shown.
        if (result === null && note === null) return;
        note = result;
        notify();
    }

    async function loadAccess() {
        if (disposed) return;
        accessLoading = true;
        const generation = ++accessGeneration;
        const isCurrent = () => generation === accessGeneration && !disposed;
        const onExpired = () => { if (isCurrent()) expireSession(); };
        try {
            // A dead session shows here first when nothing else is fetched.
            const s = await accessSummary().catch(err => {
                if (isUnauthorized(err)) onExpired();
                return null;
            });
            if (!isCurrent() || options.isActive?.() === false) return;
            const result = await namespaceAccess(s, clusterWide, onExpired);
            // The page may have been left while this loaded: going on would load content nothing
            // will show.
            if (!isCurrent() || options.isActive?.() === false) return;
            access = result;
            publishAccess();
        } finally {
            if (generation === accessGeneration) accessLoading = false;
        }
    }

    function replaceResources(next: ResourceQuery[] | undefined): boolean {
        const nextKey = resourcesKey(next);
        if (nextKey === resourceKey) return false;
        resources = next;
        resourceKey = nextKey;
        access = null;
        return true;
    }

    const onPopState = () => handle.setLocationSearch(undefined);

    const handle: NamespaceScopeHandle = {
        state: () => current,
        subscribe(listener) {
            listeners.add(listener);
            return () => { listeners.delete(listener); };
        },
        setLocationSearch(search) {
            if (disposed) return;
            if (search === undefined && !followPopState) {
                window.addEventListener('popstate', onPopState);
                followPopState = true;
            } else if (search !== undefined && followPopState) {
                window.removeEventListener('popstate', onPopState);
                followPopState = false;
            }
            const next = search ?? window.location.search;
            const first = !started;
            if (!first && canonicalSearch(next) === canonicalSearch(appliedSearch)) return;
            started = true;
            appliedSearch = next;
            applyFromURL(next);
            // Hosts may bind their listeners after connection. Replay only the initial URL
            // choice, and only if neither the location nor the user's choice has changed.
            if (first && source === 'url') {
                queueMicrotask(() => {
                    if (!disposed && source === 'url' && appliedSearch === next) dispatchScopeChosen(target, value);
                });
            }
            if (first) void loadAccess();
        },
        select(scope, typed = false) {
            if (disposed) return;
            const v = scopeToValue(scope);
            // A typed value is a name even when it is SCOPE_ALL, which is not a valid one.
            if ((typed || scope.kind === 'namespace') && !isValidNamespaceName(v)) {
                error = `"${v}" is not a valid namespace name.`;
                notify();
                return;
            }
            error = null;
            dispatchScopeChosen(target, v);
            // Chosen again, or chosen where the page was defaulted: the choice is still written,
            // so that a reload keeps it as one.
            appliedSearch = writeScopeToURL(target, v);
            const changed = v !== value;
            value = v;
            source = 'picker';
            if (changed) note = null;
            notify();
            if (changed) void refreshNote();
        },
        setResources(next) {
            if (!disposed && replaceResources(next) && started) {
                publishAccess();
                void loadAccess();
            }
        },
        refreshAccess() {
            if (started && !access && !accessLoading) void loadAccess();
        },
        dispose() {
            disposed = true;
            noteToken++;
            accessGeneration++;
            listeners.clear();
            if (followPopState) window.removeEventListener('popstate', onPopState);
            followPopState = false;
        },
    };
    return handle;
}
