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

import { afterEach, describe, expect, it, vi } from 'vitest';
import { resetAccessSummary } from './access-api.js';
import type { AccessSummary, NamespaceAccessSummaryList, SubjectRules } from './access-api.js';
import { setApiBase } from './api.js';
import {
    canViewFlows,
    flowAccess,
    flowCandidateNamespaces,
    flowAccessForGate,
    flowAccessFrom,
    observableNamespaces,
} from './flow-access.js';
import type { FlowAccess } from './flow-access.js';

const watchFlows = {
    verbs: ['watch'], apiGroups: ['observability.antrea.io'], resources: ['flows'],
};

function rules(over: Partial<SubjectRules> = {}): SubjectRules {
    return { resourceRules: [], nonResourceRules: [], incomplete: false, ...over };
}

function summary(over: Partial<AccessSummary> = {}): AccessSummary {
    return { username: 'alice', groups: [], clusterAdmin: false, rules: rules(), namespaces: [], ...over };
}

const holding = rules({ resourceRules: [watchFlows] });

function list(items: Record<string, SubjectRules>): NamespaceAccessSummaryList {
    return { items: Object.entries(items).map(([namespace, r]) => ({ namespace, evaluationFailed: false, rules: r })) };
}

function access(over: Partial<FlowAccess> = {}): FlowAccess {
    return { namespaces: [], clusterWide: false, incomplete: false, ...over };
}

describe('flowAccessFrom', () => {
    it('is cluster-wide only when a rule says so', () => {
        expect(flowAccessFrom(summary({ rules: holding }), list({})).clusterWide).toBe(true);
        expect(flowAccessFrom(summary(), list({})).clusterWide).toBe(false);
    });

    it('does not guess cluster-wide from an incomplete rule list', () => {
        // can() would allow it. The page defaults to this scope, so a guess that turns out to be
        // refused would land the user on an error instead of on a namespace that works.
        expect(flowAccessFrom(summary({ rules: rules({ incomplete: true }) }), list({})).clusterWide)
            .toBe(false);
    });

    it('does not take a namespaced summary for a cluster-wide one', () => {
        expect(flowAccessFrom(summary({ rules: holding, namespace: 'ns-a' }), list({})).clusterWide)
            .toBe(false);
    });

    it('gives each namespace the verdict of its own rules', () => {
        const a = flowAccessFrom(null, list({
            'ns-a': holding,
            'ns-b': rules(),
            'ns-c': rules({ incomplete: true, evaluationError: 'boom' }),
        }));
        expect(a.namespaces).toEqual([
            { namespace: 'ns-a', verdict: 'allowed' },
            { namespace: 'ns-b', verdict: 'denied' },
            { namespace: 'ns-c', verdict: 'unknown' },
        ]);
    });

    it('reads a namespace whose review failed as unknown, not denied', () => {
        // What the backend reports when it could not get a review for a namespace at all.
        const failed = {
            namespace: 'ns-a',
            evaluationFailed: true,
            rules: rules({ incomplete: true, evaluationError: 'the access review for this namespace could not be evaluated' }),
        };
        const a = flowAccessFrom(null, { items: [failed] });
        expect(a.namespaces).toEqual([{ namespace: 'ns-a', verdict: 'unknown' }]);
        expect(canViewFlows(flowAccessFrom(summary({ namespaces: ['ns-a'] }), { items: [failed] }))).toBe(true);
    });

    it('is incomplete when there are more candidates than are asked about, or either could not be fetched', () => {
        const many = Array.from({ length: 11 }, (_, i) => `ns-${i}`);
        expect(flowAccessFrom(summary({ namespaces: many }), list({})).incomplete).toBe(true);
        expect(flowAccessFrom(summary(), null).incomplete).toBe(true);
        expect(flowAccessFrom(null, list({})).incomplete).toBe(true);
        expect(flowAccessFrom(summary(), list({})).incomplete).toBe(false);
    });
});

describe('flowCandidateNamespaces', () => {
    it('names the namespaces the summary derived from RoleBindings, sorted', () => {
        expect(flowCandidateNamespaces(summary({ namespaces: ['ns-b', 'ns-a'] })))
            .toEqual({ names: ['ns-a', 'ns-b'], complete: true });
        expect(flowCandidateNamespaces(summary({ namespaces: [] }))).toEqual({ names: [], complete: true });
    });

    it('asks about no more than the backend answers for, and says it left some out', () => {
        const many = Array.from({ length: 12 }, (_, i) => `ns-${String(i).padStart(2, '0')}`);
        const c = flowCandidateNamespaces(summary({ namespaces: many }));
        expect(c.names).toEqual(many.slice(0, 10));
        expect(c.complete).toBe(false);
        expect(flowCandidateNamespaces(summary({ namespaces: many.slice(0, 10) })).complete).toBe(true);
    });

    it('cannot enumerate a caller who may list namespaces, and is not complete for one', () => {
        expect(flowCandidateNamespaces(summary({ namespaces: ['*'] }))).toEqual({ names: [], complete: false });
    });

    it('has nothing for a summary that could not be fetched, which is not a denial', () => {
        expect(flowCandidateNamespaces(null)).toEqual({ names: [], complete: false });
        const old = { ...summary(), namespaces: null } as unknown as AccessSummary;
        expect(flowCandidateNamespaces(old)).toEqual({ names: [], complete: false });
    });
});

describe('canViewFlows', () => {
    it('renders the page for a caller who may observe one namespace', () => {
        // The case a cluster-wide check got wrong, and the whole point of the observed-namespace
        // selector.
        expect(canViewFlows(access({ namespaces: [{ namespace: 'flow-a', verdict: 'allowed' }] }))).toBe(true);
    });

    it('renders the page for a cluster-wide caller with no named namespace', () => {
        expect(canViewFlows(access({ clusterWide: true }))).toBe(true);
    });

    it('hides the page when every namespace is denied', () => {
        expect(canViewFlows(access({ namespaces: [{ namespace: 'flow-c', verdict: 'denied' }] }))).toBe(false);
        expect(canViewFlows(access())).toBe(false);
    });

    it('keeps the page for a namespace that is merely unknown', () => {
        // The API server could not enumerate its rules (a webhook authorizer, say). That is not
        // a denial, so it must not hide the page.
        expect(canViewFlows(access({ namespaces: [{ namespace: 'flow-c', verdict: 'unknown' }] }))).toBe(true);
    });

    it('fails open before the answer has loaded', () => {
        // Hiding a feature over a transient error is worse than showing one the Flow Aggregator
        // will refuse: authorization is its decision either way.
        expect(canViewFlows(null)).toBe(true);
    });

    it('fails open on an incomplete list, whatever the denials say', () => {
        // Only the first 10 candidates are asked about, so a caller with more who holds flows only
        // in a later one gets ten denials and an incomplete list. A list that under-reports
        // cannot show the caller holds nothing, and hiding the page would take the "namespace not
        // listed" box - written for exactly this case - with it.
        expect(canViewFlows(access({ incomplete: true }))).toBe(true);
        expect(canViewFlows(access({
            incomplete: true,
            namespaces: [{ namespace: 'flow-c', verdict: 'denied' }],
        }))).toBe(true);
    });
});

describe('observableNamespaces', () => {
    it('keeps what is not denied, and tolerates null', () => {
        expect(observableNamespaces(access({
            namespaces: [
                { namespace: 'flow-a', verdict: 'allowed' },
                { namespace: 'flow-b', verdict: 'unknown' },
                { namespace: 'flow-c', verdict: 'denied' },
            ],
        }))).toEqual(['flow-a', 'flow-b']);
        expect(observableNamespaces(null)).toEqual([]);
    });
});

describe('flowAccess and flowAccessForGate', () => {
    afterEach(() => {
        vi.unstubAllGlobals();
        setApiBase('');
        resetAccessSummary();
    });

    function stubFetch(handlers: {
        summary?: () => Response; namespaces?: () => Response; settings?: () => Response
    }) {
        const fetchMock = vi.fn().mockImplementation((url: string) => {
            const u = String(url);
            const h = u.includes('/access-summary/namespaces') ? handlers.namespaces
                : u.endsWith('/settings') ? handlers.settings : handlers.summary;
            return Promise.resolve(h ? h() : new Response('{}', { status: 500 }));
        });
        vi.stubGlobal('fetch', fetchMock);
        return fetchMock;
    }
    const json = (body: unknown) => () => new Response(JSON.stringify(body), { status: 200 });
    const calls = (m: ReturnType<typeof vi.fn>, suffix: string) =>
        m.mock.calls.filter(c => String(c[0]).includes(suffix)).length;

    it('flowAccess reads both summaries', async () => {
        const f = stubFetch({
            summary: json(summary({ namespaces: ['ns-a'] })),
            namespaces: json(list({ 'ns-a': holding })),
        });
        expect(await flowAccess()).toEqual({
            clusterWide: false,
            namespaces: [{ namespace: 'ns-a', verdict: 'allowed' }],
            incomplete: false,
        });
        expect(calls(f, '/access-summary/namespaces')).toBe(1);
    });

    it('flowAccess still reports a cluster-wide grant when the per-namespace fetch fails', async () => {
        // The case that used to leave a cluster-wide holder with no way to choose a scope.
        stubFetch({ summary: json(summary({ rules: holding, namespaces: ['ns-a'] })) });
        expect(await flowAccess()).toEqual({ clusterWide: true, namespaces: [], incomplete: true });
    });

    it('flowAccess never rejects: a failed fetch is an incomplete answer', async () => {
        stubFetch({});
        expect(await flowAccess()).toEqual({ clusterWide: false, namespaces: [], incomplete: true });
    });

    it('flowAccessForGate does not fetch the per-namespace summary for a cluster-wide caller', async () => {
        const f = stubFetch({ summary: json(summary({ rules: holding })), namespaces: json(list({})) });
        expect(await flowAccessForGate()).toEqual({ clusterWide: true, namespaces: [], incomplete: false });
        expect(calls(f, '/access-summary/namespaces')).toBe(0);
    });

    it('flowAccessForGate does not fetch it when Flow Aggregator integration is off', async () => {
        const f = stubFetch({
            summary: json(summary({ namespaces: ['ns-a'] })),
            namespaces: json(list({ 'ns-a': rules() })),
            settings: json({ features: { flowVisibilityEnabled: false } }),
        });
        // Not a denial: the entry stays and the page says why it is empty.
        expect(await flowAccessForGate()).toEqual({ clusterWide: false, namespaces: [], incomplete: true });
        expect(calls(f, '/access-summary/namespaces')).toBe(0);
    });

    it('flowAccessForGate fetches it when the integration is on, or when that is not known', async () => {
        const on = stubFetch({
            summary: json(summary({ namespaces: ['ns-a'] })),
            namespaces: json(list({})),
            settings: json({ features: { flowVisibilityEnabled: true } }),
        });
        await flowAccessForGate();
        expect(calls(on, '/access-summary/namespaces')).toBe(1);

        resetAccessSummary();
        const unknown = stubFetch({ summary: json(summary({ namespaces: ['ns-a'] })), namespaces: json(list({})) });
        await flowAccessForGate();
        expect(calls(unknown, '/access-summary/namespaces')).toBe(1);
    });

    it('flowAccessForGate fetches it for anyone else', async () => {
        const f = stubFetch({
            summary: json(summary({ namespaces: ['ns-a'] })),
            namespaces: json(list({ 'ns-a': rules() })),
        });
        expect(canViewFlows(await flowAccessForGate())).toBe(false);
        expect(calls(f, '/access-summary/namespaces')).toBe(1);
    });
});
