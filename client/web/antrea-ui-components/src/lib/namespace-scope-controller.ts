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
    SCOPE_ALL, ALL_NAMESPACES_PARAM_VALUE, parseScopeParam, dispatchScopeChosen, writeScopeToURL,
    searchWithScope, scopeToValue, valueToScope,
} from './namespace-scope.js';
import type { NamespaceScope } from './namespace-scope.js';

// The page side of the Namespace scope rules, in one place: follow the
// URL, settle on a default, validate what the user types, say what was chosen, note a Namespace
// that is not listed. It is framework-agnostic, so a Lit page, a React page and a plugin written
// in neither can all drive it. The built-in pages and the plugin-facing <antrea-namespace-scope>
// element use this same implementation.

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

/** What a host supplies to have the controller call it, instead of dispatching the scope events
 * from the page's element: for a host whose router must be told directly (see
 * createPluginNamespaceScope). */
export interface NamespaceScopeHostAdapter {
    /** Puts `namespace` (a Namespace name, '_all', or null to remove it) in the current route's
     * query, replacing the history entry. */
    writeScope(namespace: string | null): void;
    /** Records the last scope chosen for the signed-in user. */
    rememberScope(scope: string): void;
    /** The mounted page's declared permissions, used when the plugin supplies no override.
     * Read when access loads, since a plugin may be created before the host binds its page. */
    getResources?(): NamespaceScopeResources | undefined;
    /** Says that the session is dead (the API answered 401). */
    sessionExpired(): void;
}

/** The permissions of a Namespace-aware page, any-of in either form. */
export type NamespaceScopeResources = ResourceQuery[] | { anyOf: ResourceQuery[] };

export interface NamespaceScopeOptions {
    /** Where the scope events are dispatched from: the page's own element, or any node inside it
     * (the events bubble and are composed). A host adapter may handle them directly. */
    target: EventTarget;
    /** Overrides the page's declared permissions, any-of, as in `requires.resources`.
     * Absent: use the host's declaration; without one, allowed for a cluster admin and unknown
     * for anyone else. */
    resources?: NamespaceScopeResources;
}

/** Host-only options; plugin handles always use the host's navigation and session handling. */
export interface NamespaceScopeControllerOptions extends NamespaceScopeOptions {
    /** Calls the host directly instead of dispatching the events. */
    adapter?: NamespaceScopeHostAdapter;
    /** Called instead of dispatching `antrea-session-expired` from `target`, for a page that has
     * more to do when the session dies. Not for plugins. */
    onSessionExpired?: () => void;
    /** Whether the page is worth loading for right now. A load that finishes while it says no is
     * dropped, and refreshAccess() runs it again. Absent: always. Not for plugins. */
    isActive?: () => boolean;
}

export interface NamespaceScopeHandle {
    state(): NamespaceScopeState;
    /** Calls `listener` with the new state on every change. Returns an unsubscribe function. */
    subscribe(listener: (state: NamespaceScopeState) => void): () => void;
    /** Feeds it the location's query string, on the first call and each time it changes.
     * undefined: read window.location.search, and follow popstate. Pass new permissions with a
     * navigation to publish the new location and permissions together. */
    setLocationSearch(search: string | undefined, options?: { resources: NamespaceScopeResources | undefined }): void;
    /** The user chose a scope. `typed`: a typed value is checked as a Namespace name. Writes the
     * URL and reports the choice. */
    select(scope: NamespaceScope, typed?: boolean): void;
    /** Replaces the permissions the cluster-wide verdict is about. */
    setResources(resources: NamespaceScopeResources | undefined): void;
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

function queriesOf(resources: NamespaceScopeResources): ResourceQuery[] {
    return Array.isArray(resources) ? resources : resources.anyOf;
}

// Equivalent permissions from a fresh render must not clear the scope or restart its requests.
function resourcesKey(resources: NamespaceScopeResources | undefined): string | undefined {
    return resources === undefined ? undefined : JSON.stringify(queriesOf(resources)
        .map(q => JSON.stringify([q.group, q.resource, q.verb, q.name])).sort());
}

/** Creates the scope controller of one page. */
export function createNamespaceScope(options: NamespaceScopeControllerOptions): NamespaceScopeHandle {
    const { target, adapter } = options;
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
        const effective = resources ?? adapter?.getResources?.();
        if (effective) return pageVerdict(s, queriesOf(effective));
        return s?.clusterAdmin ? 'allowed' : 'unknown';
    }

    function report(v: string) {
        if (adapter) adapter.rememberScope(v === SCOPE_ALL ? ALL_NAMESPACES_PARAM_VALUE : v);
        else dispatchScopeChosen(target, v);
    }

    function write(v: string): string {
        if (!adapter) return writeScopeToURL(target, v);
        adapter.writeScope(v === SCOPE_ALL ? ALL_NAMESPACES_PARAM_VALUE : (v || null));
        return searchWithScope(appliedSearch, v);
    }

    function expireSession() {
        if (options.onSessionExpired) options.onSessionExpired();
        else if (adapter) adapter.sessionExpired();
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
            report(parsed.value);
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
            // The page's ref may have attached after create() or the first location update.
            replaceResources(resources);
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

    function replaceResources(next: NamespaceScopeResources | undefined): boolean {
        // Remember an explicit override even when it equals the current route declaration.
        resources = next;
        const nextKey = resourcesKey(resources ?? adapter?.getResources?.());
        if (nextKey === resourceKey) return false;
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
        setLocationSearch(search, options) {
            if (disposed) return;
            if (search === undefined && !followPopState) {
                window.addEventListener('popstate', onPopState);
                followPopState = true;
            } else if (search !== undefined && followPopState) {
                window.removeEventListener('popstate', onPopState);
                followPopState = false;
            }
            const next = search ?? window.location.search;
            const resourcesChanged = replaceResources(options ? options.resources : resources);
            const first = !started;
            if (first || canonicalSearch(next) !== canonicalSearch(appliedSearch)) {
                started = true;
                appliedSearch = next;
                applyFromURL(next);
            } else if (resourcesChanged) {
                publishAccess();
            }
            // Hosts may bind their listeners after connection. Replay only the initial URL
            // choice, and only if neither the location nor the user's choice has changed.
            if (first && source === 'url') {
                queueMicrotask(() => {
                    if (!disposed && source === 'url' && appliedSearch === next) report(value);
                });
            }
            if (first || resourcesChanged) void loadAccess();
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
            report(v);
            // Chosen again, or chosen where the page was defaulted: the choice is still written,
            // so that a reload keeps it as one.
            appliedSearch = write(v);
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
