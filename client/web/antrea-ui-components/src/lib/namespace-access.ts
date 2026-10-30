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

import { accessSummary, onAccessSummaryReset, verdict, GATE_FLOWS_WATCH, GATE_FLOWS_LIST } from './access-api.js';
import type { AccessSummary, Verdict } from './access-api.js';
import { apiFetchJSON, APIError } from './api.js';

/**
 * What a Namespace picker offers: whether the user holds the cluster scope, and the namespaces to
 * choose from. Derived from the cluster-scoped access summary, which knows nothing about any
 * page: the resources and verbs the verdict is about are the page's own, given to
 * namespaceAccess() as a callback (flowAccess() asks about watch on flows).
 *
 * A rendering hint and never an authorization decision: the API decides what the user gets.
 */
export interface NamespaceAccess {
    /** The page's verdict cluster-wide, as the caller's callback gave it. 'unknown' when no rule
     * grants it but the API server could not enumerate its rules, or the summary could not be
     * fetched, which is not a denial: the picker offers the cluster scope unless this is
     * 'denied', and a page defaults to it only when it is 'allowed'. */
    clusterWide: Verdict
    /** Every namespace the picker can search, sorted, or null when they are not known: the
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
export function mayNameNamespace(access: NamespaceAccess | null): boolean {
    return !!access && (!access.candidates?.length || access.cut);
}

/** How many namespaces the picker offers at once. */
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

let namespaceList: Promise<{ names: string[], cut: boolean }> | null = null;
onAccessSummaryReset(() => { namespaceList = null; });

/** Every namespace's name, sorted, up to LIST_SAFEGUARD of them. Needs the caller to be allowed to
 * list namespaces. Memoized like the access summary: the list is the one the user had when they
 * signed in or last reloaded the UI, and does not change from one visit to a page to the next. A
 * failure is not kept. */
function listNamespaceNames(): Promise<{ names: string[], cut: boolean }> {
    if (!namespaceList) {
        const p = fetchNamespaceNames();
        namespaceList = p;
        p.catch(() => { if (namespaceList === p) namespaceList = null; });
    }
    return namespaceList;
}

async function fetchNamespaceNames(): Promise<{ names: string[], cut: boolean }> {
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
 * get namespaces, which the chart's roles grant along with list. */
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
 * What a Namespace picker offers for a page whose cluster-wide verdict is given by `clusterWide`,
 * evaluated against `summary`: the cluster-scoped access summary, or null when it could not be
 * fetched (the caller fetches it, and decides what a failure means). Never rejects: a null
 * summary leaves the cluster scope 'unknown' and the candidates null, and the page still lets the
 * user name a namespace.
 *
 * The namespace list is memoized like the summary, so the pages share one fetch.
 */
export async function namespaceAccess(
    summary: AccessSummary | null, clusterWide: (summary: AccessSummary | null) => Verdict,
): Promise<NamespaceAccess> {
    const mayList = mayListNamespaces(summary);
    const listed = mayList ? await listNamespaceNames().catch(() => null) : null;
    const candidates = candidatesFromSummary(summary) ?? listed?.names ?? null;
    return {
        // `namespace` is only set on a summary evaluated for one namespace, whose rules can show
        // a namespaced Role's grant. That authorizes nothing cluster-wide, and accessSummary()
        // never asks for one, so this is a guard and not a case.
        clusterWide: summary?.namespace ? 'denied' : clusterWide(summary),
        candidates,
        mayList,
        cut: listed?.cut ?? false,
    };
}

/** What the observed-namespace selector of the flow page offers. Its cluster-wide verdict uses
 * watch on flows for live streams and list for historical queries. The selected flow service
 * authorizes and redacts every response itself. */
export async function flowAccess(follow = true): Promise<NamespaceAccess> {
    const summary = await accessSummary().catch(() => null);
    return namespaceAccess(summary, s => verdict(s, follow ? GATE_FLOWS_WATCH : GATE_FLOWS_LIST));
}

/**
 * The namespaces a picker should offer for a search: those starting with `query` (ignoring case
 * and surrounding spaces), at most one page of them, and whether there were more.
 */
export function searchNamespaces(access: NamespaceAccess | null, query: string): { items: string[], more: boolean } {
    const q = query.trim().toLowerCase();
    const matches = (access?.candidates ?? []).filter(n => n.startsWith(q));
    return { items: matches.slice(0, SELECTOR_PAGE_SIZE), more: matches.length > SELECTOR_PAGE_SIZE };
}

/** A note on the selected Namespace, which is not in the list. */
export interface ScopeNote {
    /** 'missing' when a fresh check confirmed that the Namespace does not exist; 'not-listed'
     * when it may not exist or the user may not have access. */
    kind: 'missing' | 'not-listed'
    text: string
}

/**
 * The note for a selected Namespace that is not in the list, or null for none. The page still
 * loads such a Namespace, since the list is a hint. Only when the list is complete and not empty:
 * for a user who may list Namespaces the list is every Namespace, so "does not exist", once a
 * fresh check confirms it (it may have been created since the list was loaded); otherwise the
 * Namespace may not exist or may not be accessible. `isCurrent` says whether the answer is still
 * wanted once the check returns.
 */
export async function scopeNote(
    access: NamespaceAccess | null, ns: string, isCurrent: () => boolean,
): Promise<ScopeNote | null> {
    if (!ns || !access || mayNameNamespace(access) || access.candidates?.includes(ns)) return null;
    if (!access.mayList) {
        return {
            kind: 'not-listed',
            text: `Namespace ${ns} is not in your list: it may not exist, or you may not have access`,
        };
    }
    const exists = await namespaceExists(ns);
    if (!isCurrent()) return null;
    return exists === false ? { kind: 'missing', text: `Namespace ${ns} does not exist` } : null;
}
