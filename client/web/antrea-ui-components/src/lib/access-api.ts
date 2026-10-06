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

/** Mirrors apis/v1.NamespaceAccessSummary: what `rules` says about one Namespace. Nothing else
 * differs between Namespaces, so the identity and the cluster-admin verdict are not repeated. */
export interface NamespaceAccessSummary {
    namespace: string
    /** Evaluated for `namespace`, so it includes the grants that apply cluster-wide. When the
     * review for this Namespace could not be evaluated, `incomplete` is true and
     * `evaluationError` says so: unknown, not denied. */
    rules: SubjectRules
}

/** Mirrors apis/v1.NamespaceAccessSummaryList: AccessSummary's question, for each of the
 * Namespaces a request named. */
export interface NamespaceAccessSummaryList {
    /** One entry per Namespace named, in the order they were named. */
    items: NamespaceAccessSummary[]
}

/** How many Namespaces one namespaceAccessSummaries() call can ask about. Mirrors the backend's
 * limit: each is a review against the API server. */
export const MAX_NAMESPACE_ACCESS_NAMES = 10;

let inFlight: Promise<AccessSummary> | null = null;

interface NamespaceAccessMemo {
    promise: Promise<NamespaceAccessSummaryList>
    /** When the promise fulfilled, or null while it is pending (or if it was rejected, in which
     * case the memo is gone). */
    settledAt: number | null
}
/** One memo per question: the Namespaces asked about, in order. */
const namespaceAccessMemos = new Map<string, NamespaceAccessMemo>();

/**
 * How long namespaceAccessSummaries() reuses a successful answer. It matches the backend's cache:
 * the answer is what a Namespace selector offers, so a grant added or revoked should show up
 * without the user logging out, and keeping it any longer here would defeat that. What the memo is
 * for is the features that ask the same question sharing one fetch, not sparing the backend
 * indefinitely.
 */
const NAMESPACE_ACCESS_TTL_MS = 30_000;

/**
 * How long to wait for the access summary before aborting it. The whole shell gates on this one
 * request — the nav renders no core entries and the landing route renders nothing until it
 * settles — so a request that never settles (a proxy holding the connection open, a wedged
 * backend) would leave the user staring at an empty page with no nav, no error and nothing
 * distinguishing it from a broken build. Aborting routes that into the normal fail-open path
 * instead: every gate allows, and the user gets the pre-access-summary UI.
 */
const ACCESS_SUMMARY_TIMEOUT_MS = 10_000;

/** GETs path, aborting it after ACCESS_SUMMARY_TIMEOUT_MS. */
function fetchWithTimeout<T>(path: string): Promise<T> {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), ACCESS_SUMMARY_TIMEOUT_MS);
    const p = apiFetchJSON<T>(path, { signal: controller.signal });
    // Two-arg then, not finally: finally returns a promise that re-rejects, which would be
    // unhandled on this branch. The rejection stays handled by the caller either way.
    p.then(() => clearTimeout(timer), () => clearTimeout(timer));
    return p;
}

/**
 * Fetches GET /api/v1/access-summary, memoized so the React shell and every Lit page share one
 * in-flight request. This is the single fetch: nothing else caches the result across calls.
 */
export function accessSummary(): Promise<AccessSummary> {
    if (!inFlight) {
        const p = fetchWithTimeout<AccessSummary>('access-summary');
        // Memoize successes only. A rejection left in place would disable permission gating for
        // the whole session after one transient failure — silently, since every gate fails open.
        // The catch goes on a separate branch so the rejection stays handled by the caller.
        p.catch(() => { if (inFlight === p) inFlight = null; });
        inFlight = p;
    }
    return inFlight;
}

/**
 * Fetches GET /api/v1/access-summary/namespaces: the access summary of each Namespace named, in
 * one request, memoized so that every feature asking about the same Namespaces shares it. Unlike
 * accessSummary(), a success is only reused for NAMESPACE_ACCESS_TTL_MS: what it says can change
 * during a session, and it is cheap to ask again.
 *
 * Which Namespaces to name is the caller's: the ones it is about to offer or act on, such as the
 * few a user has picked from a searchable list, and at most MAX_NAMESPACE_ACCESS_NAMES of them
 * (more rejects, as the backend would). Which resource and verb a feature asks about is its own
 * gate's business: pass each item to verdict() with it. A caller whose cluster-scoped summary
 * already grants the gate has no use for this: a cluster-wide grant holds in every Namespace.
 *
 * It costs the backend one review per Namespace, so call it when a feature needs the answer, not
 * at startup.
 */
export function namespaceAccessSummaries(namespaces: string[]): Promise<NamespaceAccessSummaryList> {
    const names = Array.from(new Set(namespaces));
    if (names.length === 0) return Promise.resolve({ items: [] });
    if (names.length > MAX_NAMESPACE_ACCESS_NAMES) {
        return Promise.reject(new RangeError(
            `at most ${MAX_NAMESPACE_ACCESS_NAMES} namespaces can be asked about at once, got ${names.length}`));
    }
    const now = Date.now();
    for (const [k, m] of namespaceAccessMemos) {
        if (m.settledAt !== null && now - m.settledAt >= NAMESPACE_ACCESS_TTL_MS) namespaceAccessMemos.delete(k);
    }
    // NUL cannot appear in a Namespace name.
    const key = names.join('\0');
    const memo = namespaceAccessMemos.get(key);
    if (memo) return memo.promise;

    const query = new URLSearchParams(names.map(ns => ['namespace', ns]));
    const entry: NamespaceAccessMemo = {
        promise: fetchWithTimeout<NamespaceAccessSummaryList>(`access-summary/namespaces?${query}`),
        settledAt: null,
    };
    namespaceAccessMemos.set(key, entry);
    // A rejection is not memoized, as for accessSummary(). The guard is for a reset that replaced
    // this entry while it was pending.
    entry.promise.then(
        () => { entry.settledAt = Date.now(); },
        () => { if (namespaceAccessMemos.get(key) === entry) namespaceAccessMemos.delete(key); },
    );
    return entry.promise;
}

/** Clears the memoized fetch, so the next accessSummary() call re-evaluates. Call this on
 * logout/re-login: permissions from a previous session must never leak into a new one. */
export function resetAccessSummary(): void {
    inFlight = null;
    namespaceAccessMemos.clear();
}

export interface ResourceQuery {
    group: string
    resource: string
    verb: string
    name?: string
}

/** What a rule list says about one query. */
export type Verdict =
    /** A rule matches. Rules are additive, so this holds even when the list is incomplete. */
    | 'allowed'
    /** No rule matches, but the list is not exhaustive (or there is none): not a denial. */
    | 'unknown'
    /** No rule matches in an exhaustive list. */
    | 'denied'

/** Anything with a rule list: an AccessSummary, or one entry of a NamespaceAccessSummaryList. */
export interface HasRules {
    rules: SubjectRules
}

/**
 * Three-way form of can(), for a caller that must tell "denied" from "cannot tell" — such as one
 * that marks a Namespace as not authorized, and so must not do it for an API server that cannot
 * enumerate its rules. A null s (fetch failed or not loaded yet) is unknown.
 */
export function verdict(s: HasRules | null, q: ResourceQuery): Verdict {
    if (s === null) return 'unknown';
    // ?? []: these marshal to null rather than [] when empty, and an older server may send that.
    const granted = (s.rules.resourceRules ?? []).some((rule) => {
        if (rule.resourceNames && rule.resourceNames.length > 0) {
            if (!q.name || !rule.resourceNames.includes(q.name)) return false;
        }
        return matches(rule.apiGroups, q.group) && matches(rule.resources, q.resource) && matches(rule.verbs, q.verb);
    });
    if (granted) return 'allowed';
    return s.rules.incomplete ? 'unknown' : 'denied';
}

/**
 * Reports whether q is granted by s.rules. Fails open (returns true) when s is null (fetch
 * failed or not loaded yet) or s.rules.incomplete is true (the server's rule list is not
 * exhaustive) — in both cases, absence of a matching rule does not mean denial.
 */
export function can(s: HasRules | null, q: ResourceQuery): boolean {
    return verdict(s, q) !== 'denied';
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
// This is a rendering hint, not an authorization decision: the Flow Aggregator is still what
// authorizes and redacts each stream, and can() fails open on an incomplete/unloaded summary, so
// this can only ever hide the entry for a user who would be refused anyway - never show it to one
// who wouldn't. watch, not list: the page only ever opens a following stream, and that is the verb
// the Flow Aggregator checks for one.
export const GATE_FLOWS_WATCH = { group: 'observability.antrea.io', resource: 'flows', verb: 'watch' };

// Whether the caller may view flow data. TEMPORARY: while the observed-Namespace selector does
// not exist yet, clusterWide=true is the only scope Flow Visibility can ever request, so the only
// two outcomes reachable are a fully disclosed stream (for a caller holding flows cluster-wide)
// or a 403. Showing the page to the first group only is exactly what a cluster-wide watch grant
// partitions. Revisit once the selector lands and a caller can request their own Namespace's
// scope instead, at which point a namespaced grant becomes worth rendering for.
//
// Hence the s.namespace guard: a namespace-scoped summary can show a namespaced Role's grant on
// flows too, which authorizes nothing cluster-wide, so trusting can() alone would read a caller's
// own-Namespace grant as if it were cluster-wide. Checked here rather than left to the caller,
// since a summary is cluster-scoped by default (accessSummary() takes no namespace) and this is
// exactly the check that would silently do the wrong thing the one time that stops being true.
export function canViewFlows(s: AccessSummary | null): boolean {
    if (s?.namespace) return false;
    return can(s, GATE_FLOWS_WATCH);
}

export function canViewSummary(s: AccessSummary | null): boolean {
    return can(s, GATE_AGENT_INFO_LIST) || can(s, GATE_CONTROLLER_INFO_GET) || canNonResource(s, GATE_FEATUREGATES);
}
