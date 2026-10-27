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

import { isValidNamespaceName } from './namespace-access.js';

// The scope of a Namespace-aware page (docs/design/namespace-scope-ux.md): All Namespaces, or one
// Namespace. These are shared by every such page, so that a link from one can carry its scope to
// another.

/** The picker's value for All Namespaces. Not a namespace name, so it cannot collide with one: a
 * namespace may not contain an underscore. */
export const SCOPE_ALL = '__cluster_wide__';

// The query parameter the scope travels in on the page URL, so a refresh or a shared link keeps
// it: `?namespace=<name>`, or `?namespace=_all` for All Namespaces (`_all` cannot be a Namespace
// name). Nothing else uses `namespace`.
export const NAMESPACE_PARAM = 'namespace';
export const ALL_NAMESPACES_PARAM_VALUE = '_all';
export const ALL_NAMESPACES_LABEL = 'All Namespaces';

/** The event a page dispatches after it rewrites its own query string, so a host router can
 * adopt the new location: the page changes the URL with history.replaceState, which no router
 * is told about. */
export const SCOPE_CHANGE_EVENT = 'antrea-namespace-scope-change';
/** Detail of SCOPE_CHANGE_EVENT. */
export interface ScopeChangeDetail {
    /** The page's new query string, with its leading "?" (empty when there is none). */
    search: string;
}

/** The event a page dispatches when it takes a valid scope from its picker or its URL, so the
 * host can remember it as the last scope chosen. Not for a default, which is not a choice. */
export const SCOPE_CHOSEN_EVENT = 'antrea-namespace-scope-chosen';
/** Detail of SCOPE_CHOSEN_EVENT. */
export interface ScopeChosenDetail {
    /** The scope as it appears in a URL: a Namespace name, or `_all`. */
    scope: string;
}

export type ParsedScope =
    | { kind: 'none' }
    | { kind: 'invalid', message: string }
    | { kind: 'scope', value: string };

/** Reads the scope out of a query string. `namespace` is `_all` or a valid Namespace name,
 * given once. The value of a scope is SCOPE_ALL or a Namespace name. */
export function parseScopeParam(search: string): ParsedScope {
    const values = new URLSearchParams(search).getAll(NAMESPACE_PARAM);
    if (values.length === 0) return { kind: 'none' };
    if (values.length > 1) {
        return { kind: 'invalid', message: `The URL names more than one namespace ("${NAMESPACE_PARAM}" is given ${values.length} times). Pick one.` };
    }
    const [value] = values;
    if (value === ALL_NAMESPACES_PARAM_VALUE) return { kind: 'scope', value: SCOPE_ALL };
    if (value === '') return { kind: 'invalid', message: 'The URL names an empty namespace. Pick one.' };
    if (!isValidNamespaceName(value)) {
        return { kind: 'invalid', message: `"${value}" in the URL is not a valid namespace name. Pick one.` };
    }
    return { kind: 'scope', value };
}

/** Says that a page took a valid scope from its picker or its URL. */
export function dispatchScopeChosen(host: EventTarget, value: string): void {
    const scope = value === SCOPE_ALL ? ALL_NAMESPACES_PARAM_VALUE : value;
    host.dispatchEvent(new CustomEvent<ScopeChosenDetail>(SCOPE_CHOSEN_EVENT, {
        detail: { scope },
        bubbles: true,
        composed: true,
    }));
}

/**
 * Writes `scope` ('' for none) to the URL, in place: this is page state a refresh should keep,
 * not a navigation worth a history entry. Only for a scope the user chose. Returns the query
 * string the URL now has.
 */
export function writeScopeToURL(host: EventTarget, scope: string): string {
    const url = new URL(window.location.href);
    url.searchParams.delete(NAMESPACE_PARAM);
    if (scope === SCOPE_ALL) url.searchParams.set(NAMESPACE_PARAM, ALL_NAMESPACES_PARAM_VALUE);
    else if (scope) url.searchParams.set(NAMESPACE_PARAM, scope);
    if (url.toString() === window.location.href) return url.search;
    window.history.replaceState(window.history.state, '', url.toString());
    // replaceState mutates the URL without telling anyone. A router that reads the location
    // through its own abstraction - react-router's useLocation(), in this repo's host - goes
    // on serving the value it last saw, so links it builds from the query string carry a
    // stale scope, or none at all on a fresh load. The host is told explicitly: dispatching
    // popstate instead would reach every listener on the window with a navigation that did
    // not happen. A host that does not listen simply keeps the URL replaceState wrote.
    host.dispatchEvent(new CustomEvent<ScopeChangeDetail>(SCOPE_CHANGE_EVENT, {
        detail: { search: url.search },
        bubbles: true,
        composed: true,
    }));
    return url.search;
}
