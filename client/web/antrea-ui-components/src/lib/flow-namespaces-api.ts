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

/** Mirrors apis/v1.FlowNamespaceAccess: one candidate namespace and whether this user may
 * observe flows in it. A candidate the user may *not* observe is reported too, so the selector
 * can show it as unavailable rather than silently omitting it. */
export interface FlowNamespaceAccess {
    namespace: string
    /** The verdict of a SelfSubjectAccessReview for watch (not list — the stream always follows)
     * on flows.observability.antrea.io in this namespace. */
    canObserve: boolean
}

/** Mirrors apis/v1.FlowNamespacesResponse: the options for the observed-namespace selector.
 * Kubernetes has no reverse lookup from a subject to the namespaces it may access, and the Flow
 * Aggregator requires every stream to name its scope, so these have to be enumerated. Like
 * AccessSummary this is a rendering hint — the Flow Aggregator authorizes every stream itself. */
export interface FlowNamespacesResponse {
    /** The candidate list with a verdict for each, sorted by name. An empty list is a real
     * answer, not an error: this user may observe flows in no namespace. */
    namespaces: FlowNamespaceAccess[]
    /** Whether the user may observe flows cluster-wide. The only scope in which nothing is
     * redacted, so the selector labels it as such. */
    clusterWide: boolean
    /** The candidate list is not exhaustive, so a namespace's absence does not mean the user
     * cannot observe flows there. Mirrors SubjectRulesReviewStatus.Incomplete. */
    incomplete: boolean
}

/**
 * Fetches GET /api/v1/flows/namespaces.
 *
 * Deliberately not memoized the way accessSummary() is: the backend already caches this per
 * session with a short TTL, the only caller is the flow-visibility page (so there is no second
 * caller to share an in-flight request with), and a module-level cache would need its own reset
 * on logout to keep one session's namespaces from being offered to the next.
 */
export async function flowNamespaces(options: RequestInit = {}): Promise<FlowNamespacesResponse> {
    const resp = await apiFetchJSON<FlowNamespacesResponse>('flows/namespaces', options);
    // A field-by-field normalization rather than a cast: Go marshals an empty slice as null on
    // some paths, and every caller here treats these as always present.
    return {
        namespaces: resp?.namespaces ?? [],
        clusterWide: resp?.clusterWide ?? false,
        incomplete: resp?.incomplete ?? false,
    };
}

/** The namespaces from a response this user may actually observe flows in. */
export function observableNamespaces(resp: FlowNamespacesResponse | null): string[] {
    return (resp?.namespaces ?? []).filter(n => n.canObserve).map(n => n.namespace);
}

/**
 * Whether to render Flow Visibility at all for this caller.
 *
 * A rendering hint, never an authorization decision: the Flow Aggregator authorizes and redacts
 * every stream itself. So this fails open — a response that has not loaded, or whose fetch
 * failed, shows the entry rather than hiding a feature over a transient error.
 *
 * It replaces a check against a cluster-wide flows watch grant, which was correct only while
 * cluster-wide was the only scope the page could request. Now that a caller can name their own
 * namespace, a namespaced grant is exactly what should render the page, and the cluster-wide
 * check would hide it from every user this feature was built for. `Incomplete` is deliberately
 * not consulted: it can only mean the list under-reports, and under-reporting is already handled
 * by failing open on the two cases above.
 */
export function canViewFlows(resp: FlowNamespacesResponse | null): boolean {
    if (!resp) return true;
    return resp.clusterWide || resp.namespaces.some(n => n.canObserve);
}
