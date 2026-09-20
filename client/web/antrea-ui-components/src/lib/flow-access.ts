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

import {
    accessSummary,
    namespaceAccessSummaries,
    verdict,
    GATE_FLOWS_WATCH,
    MAX_NAMESPACE_ACCESS_NAMES,
} from './access-api.js';
import type { AccessSummary, NamespaceAccessSummaryList, Verdict } from './access-api.js';
import { apiFetchJSON } from './api.js';
import { apiFetchAppSettings } from './auth-api.js';

/** Whether this user may observe flows in one namespace, as far as can be told. */
export interface FlowNamespaceAccess {
    namespace: string
    /** 'allowed' when a rule grants watch on flows there (watch, not list: the stream always
     * follows). 'unknown' when none does but the API server could not enumerate its rules, which
     * is not a denial. 'denied' otherwise. */
    verdict: Verdict
}

/**
 * Where this user may observe flows: what the observed-namespace selector offers and what the nav
 * gates on. Derived from the access summary APIs, which know nothing about flows: the resource and
 * verb asked about are GATE_FLOWS_WATCH, here and nowhere in the backend.
 *
 * Like the summaries it comes from, a rendering hint and never an authorization decision: the Flow
 * Aggregator authorizes and redacts every stream itself.
 */
export interface FlowAccess {
    /** The user holds watch on flows cluster-wide. The only scope in which nothing is redacted,
     * so the selector labels it as such and defaults to it. Only ever 'allowed', never a guess. */
    clusterWide: boolean
    /** The candidate namespaces with a verdict each, sorted by name. */
    namespaces: FlowNamespaceAccess[]
    /** The list may be short, so a namespace's absence from it does not mean the user cannot
     * observe flows there: there are more candidates than one request asks about, they cannot be
     * enumerated, or the summary or the answer could not be fetched. */
    incomplete: boolean
    /** Every namespace the selector can search, sorted, or null when they are not known: the
     * summary could not be fetched, or it says "*" and the namespaces could not be listed. Not a
     * verdict on any of them. */
    candidates: string[] | null
}

/** How many namespaces the selector offers at once, and how many a batch asks about. */
const SELECTOR_PAGE_SIZE = MAX_NAMESPACE_ACCESS_NAMES;

/** The namespaces an access summary names, or null when it names none that can be searched:
 * the summary is missing, or it says "*" (the caller may list namespaces) and so carries no names. */
function candidatesFromSummary(summary: AccessSummary | null): string[] | null {
    const all = summary?.namespaces;
    if (!all || (all.length === 1 && all[0] === '*')) return null;
    return [...all].sort();
}

/** Whether the caller may list namespaces, which is what the summary reports as "*". */
function mayListNamespaces(summary: AccessSummary | null): boolean {
    const all = summary?.namespaces;
    return !!all && all.length === 1 && all[0] === '*';
}

/** Every namespace's name, sorted. Needs the caller to be allowed to list namespaces. */
async function listNamespaceNames(): Promise<string[]> {
    const res = await apiFetchJSON<{ items: { metadata: { name: string } }[] }>('k8s/api/v1/namespaces');
    return (res.items ?? []).map(i => i.metadata.name).sort();
}

/**
 * The namespaces worth asking the backend about, and whether that is all of them.
 *
 * The backend answers for the namespaces it is given and does not look for any. At most
 * MAX_NAMESPACE_ACCESS_NAMES are asked about, since each is a review against the API server.
 * Candidates that are not known have none to ask about, which is not a denial, so it is not
 * complete either.
 */
export function flowCandidateNamespaces(candidates: string[] | null): { names: string[]; complete: boolean } {
    if (!candidates) return { names: [], complete: false };
    return {
        names: candidates.slice(0, MAX_NAMESPACE_ACCESS_NAMES),
        complete: candidates.length <= MAX_NAMESPACE_ACCESS_NAMES,
    };
}

/** Derives FlowAccess from the two summaries. Either is null when it could not be fetched. The
 * second is the answer for the namespaces flowCandidateNamespaces named. */
export function flowAccessFrom(
    summary: AccessSummary | null,
    list: NamespaceAccessSummaryList | null,
    candidates: string[] | null = candidatesFromSummary(summary),
): FlowAccess {
    return {
        // `namespace` is only set on a summary evaluated for one namespace, whose rules can show
        // a namespaced Role's grant. That authorizes nothing cluster-wide, and accessSummary()
        // never asks for one, so this is a guard and not a case.
        clusterWide: summary !== null && !summary.namespace
            && verdict(summary, GATE_FLOWS_WATCH) === 'allowed',
        namespaces: (list?.items ?? []).map(item => ({
            namespace: item.namespace,
            verdict: verdict(item, GATE_FLOWS_WATCH),
        })),
        incomplete: list === null || !flowCandidateNamespaces(candidates).complete,
        candidates,
    };
}

/** How long the gate waits for the settings before going on without them. */
const SETTINGS_TIMEOUT_MS = 5000;

/** The app settings, or null when they could not be fetched in time. A request that hangs would
 * otherwise leave the gate unresolved, and the nav and pages behind it unrendered. */
async function settingsOrNull(): Promise<Awaited<ReturnType<typeof apiFetchAppSettings>> | null> {
    let timer: ReturnType<typeof setTimeout> | undefined;
    const timeout = new Promise<null>(resolve => { timer = setTimeout(() => resolve(null), SETTINGS_TIMEOUT_MS); });
    try {
        return await Promise.race([apiFetchAppSettings().catch(() => null), timeout]);
    } finally {
        clearTimeout(timer);
    }
}

async function loadFlowAccess(forGate: boolean): Promise<FlowAccess> {
    // Only the gate asks, and it asks in parallel: the settings are what says whether there is
    // any Flow Aggregator to observe flows with, so a deployment without one is not made to pay
    // for a review per namespace at every login. The page itself is only reached by someone who
    // went looking for it, and says why it is empty.
    const settings = forGate ? settingsOrNull() : null;
    // Both fail open: a fetch that failed leaves a hole that flowAccessFrom reports as
    // incomplete, which shows the page and lets the Flow Aggregator answer.
    const summary = await accessSummary().catch(() => null);
    const clusterWide = flowAccessFrom(summary, null).clusterWide;
    // A caller who may list namespaces has no names in the summary, so the page lists them. The
    // gate does not: it is only deciding whether to show the entry.
    const candidates = candidatesFromSummary(summary)
        ?? (!forGate && mayListNamespaces(summary) ? await listNamespaceNames().catch(() => null) : null);
    if (clusterWide && (forGate || candidates !== null)) {
        // The grant holds in every namespace, so there is nothing to review.
        return { clusterWide, namespaces: [], incomplete: false, candidates };
    }
    if (forGate && (await settings)?.features?.flowVisibilityEnabled === false) {
        return { clusterWide, namespaces: [], incomplete: true, candidates };
    }
    if (!forGate) {
        return { clusterWide, namespaces: [], incomplete: candidates === null, candidates };
    }
    const list = await namespaceAccessSummaries(flowCandidateNamespaces(candidates).names).catch(() => null);
    return flowAccessFrom(summary, list, candidates);
}

/**
 * Where this user may observe flows, for the observed-namespace selector. Never rejects.
 *
 * Both summaries are memoized (and the second only briefly), so the shell's gate and the page
 * share one fetch of each. The second is not memoized when a namespace's review failed, which
 * reads as 'unknown' here, so the next call asks again.
 */
export function flowAccess(): Promise<FlowAccess> {
    return loadFlowAccess(false);
}

/**
 * Where this user may observe flows, for the nav gate. Never rejects.
 *
 * The per-namespace summary costs the backend one review per candidate, so it is not fetched
 * where its answer would not change what the gate does. For a caller who holds the grant
 * cluster-wide, which holds in every namespace, and for a deployment with Flow Aggregator
 * integration off, where the page can only say so: the entry stays, as it always has, and the
 * answer is incomplete. Those results list no namespaces, and the page asks for its own.
 */
export function flowAccessForGate(): Promise<FlowAccess> {
    return loadFlowAccess(true);
}

/**
 * The namespaces a selector should offer for a search: those starting with `query`, at most one
 * page of them.
 */
export function searchFlowNamespaces(access: FlowAccess | null, query: string): string[] {
    const q = query.trim().toLowerCase();
    return (access?.candidates ?? []).filter(n => n.startsWith(q)).slice(0, SELECTOR_PAGE_SIZE);
}

/** The namespaces a selector should offer: those not denied. */
export function observableNamespaces(access: FlowAccess | null): string[] {
    return (access?.namespaces ?? []).filter(n => n.verdict !== 'denied').map(n => n.namespace);
}

/**
 * Whether to render Flow Visibility at all for this caller.
 *
 * A rendering hint, never an authorization decision: the Flow Aggregator authorizes and redacts
 * every stream itself. So this fails open - an answer that has not loaded, or whose fetch failed,
 * shows the entry rather than hiding a feature over a transient error. Only a denial hides it,
 * and only a complete one: a namespace that is merely unknown, because the API server could not
 * enumerate its rules, keeps the page.
 *
 * An incomplete list fails open too, and has to: a list known to be short cannot show that the
 * caller holds nothing. The case is real rather than theoretical - only the first 10 candidates
 * are asked about (see flowCandidateNamespaces), so a caller with more who holds flows only in a
 * later one gets ten denials and `incomplete`. Consulting only the denials would hide the page
 * from them, and with it the "namespace not listed" box that exists for exactly this case.
 *
 * That leans on the list being incomplete only when it is known to be short: a list derived from
 * RoleBindings and small enough to ask about whole is exhaustive for RBAC. If every such list were
 * incomplete - nearly every namespaced caller's - this gate would never close for the caller it
 * exists to close for: one holding no flows grant at all, who belongs on Settings rather than on
 * a page with nothing to show.
 */
export function canViewFlows(access: FlowAccess | null): boolean {
    if (!access || access.incomplete) return true;
    return access.clusterWide || access.namespaces.some(n => n.verdict !== 'denied');
}
