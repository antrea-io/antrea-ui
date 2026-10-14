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

import { accessSummary, verdict, GATE_FLOWS_WATCH } from './access-api.js';
import type { AccessSummary, Verdict } from './access-api.js';
import { apiFetchJSON } from './api.js';

/**
 * What the observed-namespace selector offers: whether the user holds the cluster scope, and the
 * namespaces to choose from. Derived from the cluster-scoped access summary, which knows nothing
 * about flows: the resource and verb asked about are GATE_FLOWS_WATCH, here and nowhere in the
 * backend.
 *
 * A rendering hint and never an authorization decision: the Flow Aggregator authorizes and
 * redacts every stream itself.
 */
export interface FlowAccess {
    /** Whether the user holds watch on flows cluster-wide, the only scope in which nothing is
     * redacted. 'unknown' when no rule grants it but the API server could not enumerate its rules,
     * or the summary could not be fetched, which is not a denial: the selector offers the cluster
     * scope unless this is 'denied', and defaults to it only when it is 'allowed'. */
    clusterWide: Verdict
    /** Every namespace the selector can search, sorted, or null when they are not known: the
     * summary could not be fetched, or it says "*" and the namespaces could not be listed. */
    candidates: string[] | null
}

/** How many namespaces the selector offers at once. */
const SELECTOR_PAGE_SIZE = 10;

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
 * What the observed-namespace selector offers. Never rejects: a fetch that failed leaves the
 * cluster scope 'unknown' and the candidates null, and the page still lets the user name a
 * namespace.
 *
 * The summary is memoized, so the shell's gate and the page share one fetch.
 */
export async function flowAccess(): Promise<FlowAccess> {
    const summary = await accessSummary().catch(() => null);
    const candidates = candidatesFromSummary(summary)
        ?? (mayListNamespaces(summary) ? await listNamespaceNames().catch(() => null) : null);
    return {
        // `namespace` is only set on a summary evaluated for one namespace, whose rules can show
        // a namespaced Role's grant. That authorizes nothing cluster-wide, and accessSummary()
        // never asks for one, so this is a guard and not a case.
        clusterWide: summary?.namespace ? 'denied' : verdict(summary, GATE_FLOWS_WATCH),
        candidates,
    };
}

/**
 * The namespaces a selector should offer for a search: those starting with `query`, at most one
 * page of them.
 */
export function searchFlowNamespaces(access: FlowAccess | null, query: string): string[] {
    const q = query.trim().toLowerCase();
    return (access?.candidates ?? []).filter(n => n.startsWith(q)).slice(0, SELECTOR_PAGE_SIZE);
}
