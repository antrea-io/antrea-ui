/**
 * Copyright 2026 Antrea Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

// Permission gating for plugins, shared by every host: the `requires` a plugin can put on a
// route, sidebar entry or Overview tab, and the access API a host exposes to plugin code as
// window.__antreaPluginHost.access. @antrea/ui-plugin-sdk re-exports the types; hosts use the
// functions. See docs/plugins.md ("Permission gating").

import {
    accessibleNamespaces, can, namespaceAccessSummaries, pageVerdict, reachesNamespace, verdict,
    type AccessSummary, type NamespaceAccessSummary, type ResourceQuery, type Verdict,
} from './access-api.js';

/** The permissions a requirement lists: a plain array (every query must pass, or with
 * `namespaceScope` any of them), `{ allOf }` (every query), or `{ anyOf }` (at least one). */
export type PluginResourceRequirement = ResourceQuery[] | { allOf: ResourceQuery[] } | { anyOf: ResourceQuery[] };

/** Who may see a plugin item. Mirrors the Go PluginAccessRequirement in apis/v1/plugins.go, which
 * carries it on manifest routes.
 *
 * `clusterAdmin: true` decides alone: `namespaceScope` and `resources` are ignored. Without
 * `namespaceScope`, `resources` must hold cluster-wide. With it, the item is for a Namespace-aware
 * page: its verdict comes from `resources` (allowed if any query is allowed, denied if all are
 * denied; with no `resources`, allowed only for a cluster admin and unknown for anyone else), and
 * the item shows unless that verdict is denied, or the user can reach at least one Namespace
 * (reachesNamespace()). A hint: Kubernetes RBAC still authorizes every call. `false` for either
 * flag means "not written". */
export interface PluginAccessRequirement {
    /** true: only users whose access summary reports clusterAdmin. */
    clusterAdmin?: boolean;
    /** true: the page is Namespace-aware and shows to users who can reach a Namespace. */
    namespaceScope?: boolean;
    /** An unknown verdict counts as passing, as for the built-in pages. */
    resources?: PluginResourceRequirement;
}

// Stands in for a malformed requirement and always denies: its author meant to restrict the
// item, so a typo must not show it to everyone.
const INVALID: PluginAccessRequirement = Object.freeze({});

function isResourceQuery(value: unknown): value is ResourceQuery {
    if (typeof value !== 'object' || value === null || Array.isArray(value)) return false;
    if (Object.keys(value).some(key => !['group', 'resource', 'verb', 'name'].includes(key))) return false;
    const q = value as { group?: unknown; resource?: unknown; verb?: unknown; name?: unknown };
    return typeof q.group === 'string' && typeof q.resource === 'string' && q.resource !== ''
        && typeof q.verb === 'string' && q.verb !== ''
        && (q.name === undefined || typeof q.name === 'string');
}

function isQueryList(value: unknown): value is ResourceQuery[] {
    return Array.isArray(value) && value.length > 0 && value.every(isResourceQuery);
}

// Not empty, and `allOf` only without `namespaceScope`: a Namespace-aware page's verdict is any-of.
function isResourceRequirement(value: unknown, namespaceScope: boolean): value is PluginResourceRequirement {
    if (Array.isArray(value)) return isQueryList(value);
    if (typeof value !== 'object' || value === null) return false;
    const keys = Object.keys(value);
    if (keys.length !== 1) return false;
    const { allOf, anyOf } = value as { allOf?: unknown; anyOf?: unknown };
    if (keys[0] === 'anyOf') return isQueryList(anyOf);
    return keys[0] === 'allOf' && !namespaceScope && isQueryList(allOf);
}

function isRequirement(value: unknown): value is PluginAccessRequirement {
    if (typeof value !== 'object' || value === null || Array.isArray(value)) return false;
    if (Object.keys(value).some(key => !['clusterAdmin', 'namespaceScope', 'resources'].includes(key))) return false;
    const r = value as { clusterAdmin?: unknown; namespaceScope?: unknown; resources?: unknown };
    if (r.clusterAdmin !== undefined && typeof r.clusterAdmin !== 'boolean') return false;
    if (r.namespaceScope !== undefined && typeof r.namespaceScope !== 'boolean') return false;
    return r.resources === undefined || isResourceRequirement(r.resources, r.namespaceScope === true);
}

/**
 * Turns a plugin-supplied `requires` value into the list of requirements a host stores with the
 * item (all must hold). Absent means ungated (an empty list). A malformed value is logged, naming
 * `what`, and replaced with a requirement that always denies.
 */
export function parseAccessRequirements(requires: unknown, what: string): PluginAccessRequirement[] {
    if (requires === undefined || requires === null) return [];
    if (isRequirement(requires)) return [requires];
    console.error(`plugin ${what} has a malformed "requires" (${JSON.stringify(requires)}), hiding it`);
    return [INVALID];
}

function queriesOf(resources: PluginResourceRequirement): ResourceQuery[] {
    if (Array.isArray(resources)) return resources;
    return 'anyOf' in resources ? resources.anyOf : resources.allOf;
}

function resourcesHold(summary: AccessSummary, resources: PluginResourceRequirement): boolean {
    const queries = queriesOf(resources);
    const any = !Array.isArray(resources) && 'anyOf' in resources;
    return any ? queries.some(q => can(summary, q)) : queries.every(q => can(summary, q));
}

function meetsRequirement(summary: AccessSummary, requirement: PluginAccessRequirement): boolean {
    if (requirement.clusterAdmin) return summary.clusterAdmin === true;
    const { resources } = requirement;
    if (!requirement.namespaceScope) return !resources || resourcesHold(summary, resources);
    const pageVerdictOfRequirement = resources
        ? pageVerdict(summary, queriesOf(resources))
        : (summary.clusterAdmin ? 'allowed' : 'unknown');
    return pageVerdictOfRequirement !== 'denied' || reachesNamespace(summary);
}

/**
 * Whether `summary` meets every requirement. A null summary (the fetch failed) fails open, like
 * the built-in pages: gating is a UI hint, and Kubernetes RBAC still authorizes every API call a
 * plugin makes. Callers decide what to do before the summary has loaded; hosts hide gated items.
 */
export function meetsAccessRequirements(
    summary: AccessSummary | null,
    requirements: readonly PluginAccessRequirement[] | undefined,
): boolean {
    return (requirements ?? []).every(requirement => {
        if (requirement === INVALID) return false;
        if (summary === null) return true;
        return meetsRequirement(summary, requirement);
    });
}

/** Whether any requirement marks its item as a Namespace-aware page (and the user is not held to
 * clusterAdmin alone, which ignores `namespaceScope`). Hosts use it to carry the last scope in the
 * item's navigation link. */
export function isNamespaceAware(requirements: readonly PluginAccessRequirement[] | undefined): boolean {
    return (requirements ?? []).some(r => r !== INVALID && r.namespaceScope === true && !r.clusterAdmin);
}

/** The access API a host exposes to plugin code as window.__antreaPluginHost.access. */
export interface PluginHostAccess {
    /** True once the host has an answer: a summary, or a failed fetch (summary() stays null). */
    loaded(): boolean;
    /** Resolves with summary() once loaded() is true. */
    ready(): Promise<AccessSummary | null>;
    summary(): AccessSummary | null;
    /** Fails open on an unknown verdict or a null summary, like the built-in pages. */
    can(query: ResourceQuery): boolean;
    verdict(query: ResourceQuery): Verdict;
    isClusterAdmin(): boolean;
    /** null means all Namespaces, or unknown. */
    accessibleNamespaces(): string[] | null;
    /** The same evaluation the host applies to a route's, sidebar entry's or tab's `requires`. */
    meets(requirement: PluginAccessRequirement): boolean;
    /** The user's rules in one Namespace, or null if they can't be fetched. */
    forNamespace(namespace: string): Promise<NamespaceAccessSummary | null>;
    /** Called whenever loaded() or summary() changes. Returns an unsubscribe function. */
    subscribe(listener: (summary: AccessSummary | null) => void): () => void;
}

/**
 * Creates a host's PluginHostAccess, plus the `publish` function the host calls whenever its own
 * access state changes, so plugins and the host's nav see the same summary.
 */
export function createPluginHostAccess(): {
    access: PluginHostAccess;
    publish(loaded: boolean, summary: AccessSummary | null): void;
} {
    let loaded = false;
    let summary: AccessSummary | null = null;
    const listeners = new Set<(summary: AccessSummary | null) => void>();
    let readyWaiters: ((summary: AccessSummary | null) => void)[] = [];

    const access: PluginHostAccess = {
        loaded: () => loaded,
        ready: () => loaded ? Promise.resolve(summary) : new Promise(resolve => readyWaiters.push(resolve)),
        summary: () => summary,
        can: query => can(summary, query),
        verdict: query => verdict(summary, query),
        isClusterAdmin: () => summary?.clusterAdmin === true,
        accessibleNamespaces: () => accessibleNamespaces(summary),
        meets: requirement => meetsAccessRequirements(summary, parseAccessRequirements(requirement, 'access.meets() call')),
        forNamespace: async namespace => {
            await access.ready();
            try {
                const list = await namespaceAccessSummaries([namespace]);
                return list.items.find(item => item.namespace === namespace) ?? null;
            } catch (e) {
                console.error(`failed to fetch access summary for Namespace "${namespace}"`, e);
                return null;
            }
        },
        subscribe: listener => {
            listeners.add(listener);
            return () => { listeners.delete(listener); };
        },
    };

    function publish(nextLoaded: boolean, nextSummary: AccessSummary | null): void {
        if (nextLoaded === loaded && nextSummary === summary) return;
        loaded = nextLoaded;
        summary = nextSummary;
        if (loaded) {
            const waiters = readyWaiters;
            readyWaiters = [];
            for (const resolve of waiters) resolve(summary);
        }
        for (const listener of [...listeners]) {
            try {
                listener(summary);
            } catch (e) {
                console.error('plugin access listener failed', e);
            }
        }
    }

    return { access, publish };
}
