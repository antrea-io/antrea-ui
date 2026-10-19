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
import { apiFetchJSON, APIError } from './api.js';

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
    /** Whether the caller may list namespaces, in which case the candidates are every namespace
     * in the cluster rather than those a RoleBinding names. */
    mayList: boolean
    /** Whether listing stopped at LIST_SAFEGUARD, so that the candidates are only the first of
     * them. Counts like an unknown list: the user can type a name the list does not hold. */
    cut: boolean
}

/** Whether the user can name a namespace by typing it: the candidates are empty, unknown or
 * cut, and so no absence from them says anything. */
export function mayNameNamespace(access: FlowAccess | null): boolean {
    return !!access && (!access.candidates?.length || access.cut);
}

/** How many namespaces the selector offers at once. */
export const SELECTOR_PAGE_SIZE = 10;

/** The most namespaces listed for a user who may list them. Only a safeguard: the list is not
 * capped for any real cluster, and a response that says there are more is treated as cut. */
export const LIST_SAFEGUARD = 10000;

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

/** Every namespace's name, sorted, up to LIST_SAFEGUARD of them. Needs the caller to be allowed to
 * list namespaces. */
async function listNamespaceNames(): Promise<{ names: string[], cut: boolean }> {
    const res = await apiFetchJSON<{
        items: { metadata: { name: string } }[]
        metadata?: { continue?: string }
    }>(`k8s/api/v1/namespaces?limit=${LIST_SAFEGUARD}`);
    return {
        names: (res.items ?? []).map(i => i.metadata.name).sort(),
        cut: !!res.metadata?.continue,
    };
}

/** Whether a namespace exists, asked of the API again: a list loaded earlier may predate it.
 * Null when the answer could not be had, which is not a "no". Needs the caller to be allowed to
 * get namespaces, as one who may list them is. */
export async function namespaceExists(name: string): Promise<boolean | null> {
    try {
        await apiFetchJSON(`k8s/api/v1/namespaces/${encodeURIComponent(name)}`);
        return true;
    } catch (e) {
        return e instanceof APIError && e.code === 404 ? false : null;
    }
}

// A Namespace name is an RFC 1123 label.
const NAMESPACE_NAME = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;

/** Whether `name` is a valid Namespace name: lowercase alphanumerics and '-', at most 63
 * characters, starting and ending with an alphanumeric. */
export function isValidNamespaceName(name: string): boolean {
    return name.length <= 63 && NAMESPACE_NAME.test(name);
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
    const mayList = mayListNamespaces(summary);
    const listed = mayList ? await listNamespaceNames().catch(() => null) : null;
    const candidates = candidatesFromSummary(summary) ?? listed?.names ?? null;
    return {
        // `namespace` is only set on a summary evaluated for one namespace, whose rules can show
        // a namespaced Role's grant. That authorizes nothing cluster-wide, and accessSummary()
        // never asks for one, so this is a guard and not a case.
        clusterWide: summary?.namespace ? 'denied' : verdict(summary, GATE_FLOWS_WATCH),
        candidates,
        mayList,
        cut: listed?.cut ?? false,
    };
}

/**
 * The namespaces a selector should offer for a search: those starting with `query` (ignoring case
 * and surrounding spaces), at most one page of them, and whether there were more.
 */
export function searchFlowNamespaces(access: FlowAccess | null, query: string): { items: string[], more: boolean } {
    const q = query.trim().toLowerCase();
    const matches = (access?.candidates ?? []).filter(n => n.startsWith(q));
    return { items: matches.slice(0, SELECTOR_PAGE_SIZE), more: matches.length > SELECTOR_PAGE_SIZE };
}
