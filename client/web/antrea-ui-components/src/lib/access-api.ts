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

import { apiFetchJSON } from './api.js';

/** Mirrors apis/v1.AccessSummary's embedded authorizationv1.PolicyRule. */
export interface ResourceRule {
    verbs: string[]
    apiGroups: string[]
    resources: string[]
    resourceNames?: string[]
}

/** Mirrors apis/v1.AccessSummary's embedded authorizationv1.NonResourceRule. */
export interface NonResourceRule {
    verbs: string[]
    nonResourceURLs: string[]
}

/** Mirrors authorizationv1.SubjectRulesReviewStatus. */
export interface SubjectRules {
    resourceRules: ResourceRule[]
    nonResourceRules: NonResourceRule[]
    /** The API server is telling us this list is not exhaustive: gate nothing on its absence. */
    incomplete: boolean
    evaluationError?: string
}

/** Mirrors apis/v1.AccessSummary. What the logged-in user is allowed to do, so the frontend can
 * hide UI it would only get a 403 from. A rendering hint, never an authorization decision: every
 * real request is still authorized by the API server. */
export interface AccessSummary {
    username: string
    groups: string[]
    clusterAdmin: boolean
    /** What `rules` was evaluated for. Absent means cluster scope. */
    namespace?: string
    rules: SubjectRules
    /** Namespaces the user may have access to, or ["*"] for cluster-wide. */
    namespaces: string[]
}

let inFlight: Promise<AccessSummary> | null = null;

/**
 * How long to wait for the access summary before aborting it. The whole shell gates on this one
 * request — the nav renders no core entries and the landing route renders nothing until it
 * settles — so a request that never settles (a proxy holding the connection open, a wedged
 * backend) would leave the user staring at an empty page with no nav, no error and nothing
 * distinguishing it from a broken build. Aborting routes that into the normal fail-open path
 * instead: every gate allows, and the user gets the pre-access-summary UI.
 */
const ACCESS_SUMMARY_TIMEOUT_MS = 10_000;

/**
 * Fetches GET /api/v1/access-summary, memoized so the React shell and every Lit page share one
 * in-flight request. This is the single fetch: nothing else caches the result across calls.
 */
export function accessSummary(): Promise<AccessSummary> {
    if (!inFlight) {
        const controller = new AbortController();
        const timer = setTimeout(() => controller.abort(), ACCESS_SUMMARY_TIMEOUT_MS);
        const p = apiFetchJSON<AccessSummary>('access-summary', { signal: controller.signal });
        // Two-arg then, not finally: finally returns a promise that re-rejects, which would be
        // unhandled on this branch. The rejection stays handled by the caller either way.
        p.then(() => clearTimeout(timer), () => clearTimeout(timer));
        // Memoize successes only. A rejection left in place would disable permission gating for
        // the whole session after one transient failure — silently, since every gate fails open.
        // The catch goes on a separate branch so the rejection stays handled by the caller.
        p.catch(() => { if (inFlight === p) inFlight = null; });
        inFlight = p;
    }
    return inFlight;
}

/** Clears the memoized fetch, so the next accessSummary() call re-evaluates. Call this on
 * logout/re-login: permissions from a previous session must never leak into a new one. */
export function resetAccessSummary(): void {
    inFlight = null;
}

export interface ResourceQuery {
    group: string
    resource: string
    verb: string
    name?: string
}

/**
 * Reports whether q is granted by s.rules. Fails open (returns true) when s is null (fetch
 * failed or not loaded yet) or s.rules.incomplete is true (the server's rule list is not
 * exhaustive) — in both cases, absence of a matching rule does not mean denial.
 */
export function can(s: AccessSummary | null, q: ResourceQuery): boolean {
    if (s === null || s.rules.incomplete) return true;
    // ?? []: these marshal to null rather than [] when empty, and an older server may send that.
    return (s.rules.resourceRules ?? []).some((rule) => {
        if (rule.resourceNames && rule.resourceNames.length > 0) {
            if (!q.name || !rule.resourceNames.includes(q.name)) return false;
        }
        return matches(rule.apiGroups, q.group) && matches(rule.resources, q.resource) && matches(rule.verbs, q.verb);
    });
}

export function canNonResource(s: AccessSummary | null, q: { verb: string, url: string }): boolean {
    if (s === null || s.rules.incomplete) return true;
    return (s.rules.nonResourceRules ?? []).some((rule) =>
        matches(rule.verbs, q.verb) && matchesNonResourceURL(rule.nonResourceURLs, q.url));
}

function matches(values: string[], want: string): boolean {
    return values.includes('*') || values.includes(want);
}

/**
 * Matches a nonResourceURL the way the RBAC authorizer does (NonResourceURLMatches in
 * k8s.io/kubernetes/pkg/apis/rbac/v1): "*" matches everything, an exact string matches itself,
 * and a trailing "*" matches any URL with that prefix. The prefix form is not an edge case —
 * nonResourceURLs: ["/*"] is the usual way to grant read access to every non-resource endpoint,
 * and treating it as no grant hides UI from users the API server would have served.
 */
function matchesNonResourceURL(patterns: string[], want: string): boolean {
    return patterns.some((p) => {
        if (p === '*' || p === want) return true;
        return p.endsWith('*') && want.startsWith(p.replace(/\*+$/, ''));
    });
}

/** Returns the namespaces the user may access, or null when that is all/unknown (no filter
 * should be applied): summary is null, rules are incomplete, namespaces is missing, or it is
 * ["*"]. */
export function accessibleNamespaces(s: AccessSummary | null): string[] | null {
    // !s.namespaces: the current server always sends an array, but an older one sends null when
    // it could not resolve the list, and that does not imply rules.incomplete.
    if (s === null || s.rules.incomplete || !s.namespaces) return null;
    if (s.namespaces.length === 1 && s.namespaces[0] === '*') return null;
    return s.namespaces;
}

// Gate constants live here too, so the React nav, the Lit pages and any plugin share one
// definition — one predicate per page, so the nav entry and the route guard cannot drift apart.
export const GATE_TRACEFLOW_CREATE = { group: 'crd.antrea.io', resource: 'traceflows', verb: 'create' };
export const GATE_AGENT_INFO_LIST = { group: 'crd.antrea.io', resource: 'antreaagentinfos', verb: 'list' };
// name: the summary page fetches exactly antreacontrollerinfos/antrea-controller, so a role that
// narrows the grant with resourceNames: ["antrea-controller"] does authorize that request — and
// can() rejects every resourceNames-scoped rule unless the query names the object.
export const GATE_CONTROLLER_INFO_GET = { group: 'crd.antrea.io', resource: 'antreacontrollerinfos', verb: 'get', name: 'antrea-controller' };
// A nonResourceURL because that is how antrea-ui-admin-core grants it (clusterroles.yaml), and
// the Antrea Service delegates authorization to the same RBAC.
export const GATE_FEATUREGATES = { verb: 'get', url: '/featuregates' };

export function canViewSummary(s: AccessSummary | null): boolean {
    return can(s, GATE_AGENT_INFO_LIST) || can(s, GATE_CONTROLLER_INFO_GET) || canNonResource(s, GATE_FEATUREGATES);
}

// Gates for the Overview landing page's inventory tiles — one per Kubernetes resource type it
// reads, following the same "one predicate per page" convention as the gates above.
export const GATE_NAMESPACES_LIST = { group: '', resource: 'namespaces', verb: 'list' };
export const GATE_PODS_LIST = { group: '', resource: 'pods', verb: 'list' };
export const GATE_SERVICES_LIST = { group: '', resource: 'services', verb: 'list' };
export const GATE_DEPLOYMENTS_LIST = { group: 'apps', resource: 'deployments', verb: 'list' };
export const GATE_STATEFULSETS_LIST = { group: 'apps', resource: 'statefulsets', verb: 'list' };
export const GATE_DAEMONSETS_LIST = { group: 'apps', resource: 'daemonsets', verb: 'list' };
export const GATE_K8S_NETWORKPOLICIES_LIST = { group: 'networking.k8s.io', resource: 'networkpolicies', verb: 'list' };
export const GATE_ANTREA_CLUSTERNETWORKPOLICIES_LIST = { group: 'crd.antrea.io', resource: 'clusternetworkpolicies', verb: 'list' };
export const GATE_ANTREA_NETWORKPOLICIES_LIST = { group: 'crd.antrea.io', resource: 'networkpolicies', verb: 'list' };

export function canViewOverview(s: AccessSummary | null): boolean {
    return can(s, GATE_NAMESPACES_LIST) || can(s, GATE_PODS_LIST) || can(s, GATE_SERVICES_LIST) ||
        can(s, GATE_DEPLOYMENTS_LIST) || can(s, GATE_STATEFULSETS_LIST) || can(s, GATE_DAEMONSETS_LIST) ||
        can(s, GATE_K8S_NETWORKPOLICIES_LIST) || can(s, GATE_ANTREA_CLUSTERNETWORKPOLICIES_LIST) ||
        can(s, GATE_ANTREA_NETWORKPOLICIES_LIST);
}
