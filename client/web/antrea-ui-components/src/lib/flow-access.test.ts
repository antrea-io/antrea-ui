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
import type { AccessSummary, SubjectRules } from './access-api.js';
import { setApiBase } from './api.js';
import { flowAccess, searchFlowNamespaces } from './flow-access.js';
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

function access(over: Partial<FlowAccess> = {}): FlowAccess {
    return { clusterWide: 'denied', candidates: [], ...over };
}

describe('searchFlowNamespaces', () => {
    const names = (n: number) => Array.from({ length: n }, (_, i) => `ns-${String(i).padStart(2, '0')}`);

    it('offers the first page of the candidates', () => {
        expect(searchFlowNamespaces(access({ clusterWide: 'allowed', candidates: names(25) }), '')).toEqual(names(10));
        expect(searchFlowNamespaces(access({ candidates: names(25) }), '')).toEqual(names(10));
    });

    it('matches by prefix, ignoring case and surrounding space', () => {
        const candidates = ['alpha', 'beta', 'betamax', 'gamma'];
        expect(searchFlowNamespaces(access({ clusterWide: 'allowed', candidates }), ' BETA '))
            .toEqual(['beta', 'betamax']);
    });

    it('has nothing to search when the candidates are not known', () => {
        expect(searchFlowNamespaces(access({ candidates: null }), '')).toEqual([]);
        expect(searchFlowNamespaces(null, '')).toEqual([]);
    });
});

describe('flowAccess', () => {
    afterEach(() => {
        vi.unstubAllGlobals();
        setApiBase('');
        resetAccessSummary();
    });

    function stubFetch(handlers: { summary?: () => Response; k8s?: () => Response }) {
        const fetchMock = vi.fn().mockImplementation((url: string) => {
            const h = String(url).includes('/k8s/api/v1/namespaces') ? handlers.k8s : handlers.summary;
            return Promise.resolve(h ? h() : new Response('{}', { status: 500 }));
        });
        vi.stubGlobal('fetch', fetchMock);
        return fetchMock;
    }
    const json = (body: unknown) => () => new Response(JSON.stringify(body), { status: 200 });
    const calls = (m: ReturnType<typeof vi.fn>, suffix: string) =>
        m.mock.calls.filter(c => String(c[0]).includes(suffix)).length;

    it('takes the candidates from the summary, sorted, and reviews no namespace', async () => {
        const f = stubFetch({ summary: json(summary({ namespaces: ['ns-b', 'ns-a'] })) });
        expect(await flowAccess()).toEqual({ clusterWide: 'denied', candidates: ['ns-a', 'ns-b'] });
        expect(calls(f, '/access-summary/namespaces')).toBe(0);
    });

    it('is allowed cluster-wide when a rule says so', async () => {
        stubFetch({ summary: json(summary({ rules: rules({ resourceRules: [watchFlows] }), namespaces: ['ns-a'] })) });
        expect((await flowAccess()).clusterWide).toBe('allowed');
    });

    // Not a denial: the cluster scope is still offered, since the caller may hold it through an
    // authorizer whose rules cannot be listed.
    it('is unknown cluster-wide when the rules are not exhaustive and none matches', async () => {
        stubFetch({ summary: json(summary({ rules: rules({ incomplete: true }), namespaces: ['ns-a'] })) });
        expect((await flowAccess()).clusterWide).toBe('unknown');
    });

    it('does not take a namespaced summary for a cluster-wide one', async () => {
        stubFetch({
            summary: json(summary({ namespace: 'ns-a', rules: rules({ resourceRules: [watchFlows] }) })),
        });
        expect((await flowAccess()).clusterWide).toBe('denied');
    });

    it('lists the namespaces of a caller who may list them, since the summary names none', async () => {
        const f = stubFetch({
            summary: json(summary({ namespaces: ['*'] })),
            k8s: json({ items: [{ metadata: { name: 'ns-b' } }, { metadata: { name: 'ns-a' } }] }),
        });
        expect((await flowAccess()).candidates).toEqual(['ns-a', 'ns-b']);
        expect(calls(f, '/k8s/api/v1/namespaces')).toBe(1);
    });

    it('has no candidates when that list cannot be fetched', async () => {
        stubFetch({ summary: json(summary({ namespaces: ['*'] })), k8s: () => new Response('{}', { status: 500 }) });
        expect(await flowAccess()).toEqual({ clusterWide: 'denied', candidates: null });
    });

    it('never rejects: a summary that could not be fetched leaves everything unknown', async () => {
        stubFetch({});
        expect(await flowAccess()).toEqual({ clusterWide: 'unknown', candidates: null });
    });
});
