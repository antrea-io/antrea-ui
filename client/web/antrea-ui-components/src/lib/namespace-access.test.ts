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
import {
    flowAccess, searchNamespaces, mayNameNamespace, namespaceExists, isValidNamespaceName, LIST_SAFEGUARD,
} from './namespace-access.js';
import type { NamespaceAccess } from './namespace-access.js';

const watchFlows = {
    verbs: ['watch'], apiGroups: ['observability.antrea.io'], resources: ['flows'],
};

function rules(over: Partial<SubjectRules> = {}): SubjectRules {
    return { resourceRules: [], nonResourceRules: [], incomplete: false, ...over };
}

function summary(over: Partial<AccessSummary> = {}): AccessSummary {
    return { username: 'alice', groups: [], clusterAdmin: false, rules: rules(), namespaces: [], ...over };
}

function access(over: Partial<NamespaceAccess> = {}): NamespaceAccess {
    return { clusterWide: 'denied', candidates: [], mayList: false, cut: false, ...over };
}

describe('searchNamespaces', () => {
    const names = (n: number) => Array.from({ length: n }, (_, i) => `ns-${String(i).padStart(2, '0')}`);

    it('offers the first page of the candidates, and says there are more', () => {
        expect(searchNamespaces(access({ clusterWide: 'allowed', candidates: names(25) }), ''))
            .toEqual({ items: names(10), more: true });
        expect(searchNamespaces(access({ candidates: names(25) }), '')).toEqual({ items: names(10), more: true });
    });

    it('does not say there are more when a page holds them all', () => {
        expect(searchNamespaces(access({ candidates: names(10) }), '')).toEqual({ items: names(10), more: false });
    });

    it('matches by prefix, ignoring case and surrounding space', () => {
        const candidates = ['alpha', 'beta', 'betamax', 'gamma'];
        expect(searchNamespaces(access({ clusterWide: 'allowed', candidates }), ' BETA '))
            .toEqual({ items: ['beta', 'betamax'], more: false });
    });

    it('searches the whole list, not the page that was offered', () => {
        expect(searchNamespaces(access({ candidates: names(25) }), 'ns-2')).toEqual({
            items: ['ns-20', 'ns-21', 'ns-22', 'ns-23', 'ns-24'], more: false,
        });
    });

    it('has nothing to search when the candidates are not known', () => {
        expect(searchNamespaces(access({ candidates: null }), '')).toEqual({ items: [], more: false });
        expect(searchNamespaces(null, '')).toEqual({ items: [], more: false });
    });
});

describe('mayNameNamespace', () => {
    it('is true for an empty, unknown or cut list, and false for a complete one', () => {
        expect(mayNameNamespace(access({ candidates: [] }))).toBe(true);
        expect(mayNameNamespace(access({ candidates: null }))).toBe(true);
        expect(mayNameNamespace(access({ candidates: ['a'], cut: true }))).toBe(true);
        expect(mayNameNamespace(access({ candidates: ['a'] }))).toBe(false);
    });

    it('is false while the access is not loaded', () => {
        expect(mayNameNamespace(null)).toBe(false);
    });
});

describe('isValidNamespaceName', () => {
    it('accepts an RFC 1123 label', () => {
        for (const n of ['a', 'dev', 'ns-1', '1a', 'a'.repeat(63)]) expect(isValidNamespaceName(n)).toBe(true);
    });

    it('rejects anything else', () => {
        for (const n of ['', '_all', 'Dev', '-a', 'a-', 'a_b', 'a.b', 'a b', 'a'.repeat(64)]) {
            expect(isValidNamespaceName(n)).toBe(false);
        }
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
        expect(await flowAccess()).toEqual({
            clusterWide: 'denied', candidates: ['ns-a', 'ns-b'], mayList: false, cut: false,
        });
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

    it('lists the namespaces once for as many calls', async () => {
        const f = stubFetch({
            summary: json(summary({ namespaces: ['*'] })),
            k8s: json({ items: [{ metadata: { name: 'ns-a' } }] }),
        });
        await flowAccess();
        await flowAccess();
        expect(calls(f, '/k8s/api/v1/namespaces')).toBe(1);
        resetAccessSummary();
        await flowAccess();
        expect(calls(f, '/k8s/api/v1/namespaces')).toBe(2);
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
        const got = await flowAccess();
        expect(got.candidates).toEqual(['ns-a', 'ns-b']);
        expect(got.mayList).toBe(true);
        expect(got.cut).toBe(false);
        expect(calls(f, '/k8s/api/v1/namespaces')).toBe(1);
    });

    // Not capped, but with a safeguard: a response that says there is more is a cut list.
    it('asks for at most the safeguard, and treats a continue token as a cut list', async () => {
        const f = stubFetch({
            summary: json(summary({ namespaces: ['*'] })),
            k8s: json({ items: [{ metadata: { name: 'ns-a' } }], metadata: { continue: 'next' } }),
        });
        const got = await flowAccess();
        expect(got.cut).toBe(true);
        expect(got.candidates).toEqual(['ns-a']);
        expect(String(f.mock.calls.find(c => String(c[0]).includes('/k8s/api/v1/namespaces'))![0]))
            .toContain(`limit=${LIST_SAFEGUARD}`);
    });

    // The summary's own list is used as it is, whatever the rules say: they may be incomplete
    // for every user of a webhook authorizer.
    it('uses the summary\'s namespaces as they are when the rules are incomplete', async () => {
        stubFetch({ summary: json(summary({ rules: rules({ incomplete: true }), namespaces: ['ns-b', 'ns-a'] })) });
        expect((await flowAccess()).candidates).toEqual(['ns-a', 'ns-b']);
    });

    it('has an empty list when the summary names no namespace', async () => {
        stubFetch({ summary: json(summary({ rules: rules({ incomplete: true }), namespaces: [] })) });
        expect((await flowAccess()).candidates).toEqual([]);
    });

    it('has no candidates when that list cannot be fetched', async () => {
        stubFetch({ summary: json(summary({ namespaces: ['*'] })), k8s: () => new Response('{}', { status: 500 }) });
        expect(await flowAccess()).toEqual({ clusterWide: 'denied', candidates: null, mayList: true, cut: false });
    });

    it('never rejects: a summary that could not be fetched leaves everything unknown', async () => {
        stubFetch({});
        expect(await flowAccess()).toEqual({ clusterWide: 'unknown', candidates: null, mayList: false, cut: false });
    });
});

describe('namespaceExists', () => {
    afterEach(() => {
        vi.unstubAllGlobals();
        setApiBase('');
    });

    it('is true when the namespace can be read, false on a 404, and null when it cannot be told', async () => {
        const fetchMock = vi.fn()
            .mockResolvedValueOnce(new Response('{}', { status: 200 }))
            .mockResolvedValueOnce(new Response('{}', { status: 404 }))
            .mockResolvedValueOnce(new Response('{}', { status: 500 }));
        vi.stubGlobal('fetch', fetchMock);
        expect(await namespaceExists('ns-a')).toBe(true);
        expect(await namespaceExists('ns-a')).toBe(false);
        expect(await namespaceExists('ns-a')).toBeNull();
        expect(String(fetchMock.mock.calls[0][0])).toContain('/k8s/api/v1/namespaces/ns-a');
    });
});


describe('historical flow scope permissions', () => {
    afterEach(() => { vi.unstubAllGlobals(); resetAccessSummary(); });
    it('uses list for history while retaining watch for live', async () => {
        resetAccessSummary();
        vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(summary({
            rules: rules({ resourceRules: [{ ...watchFlows, verbs: ['list'] }] }),
        })), { status: 200 })));
        expect((await flowAccess(false)).clusterWide).toBe('allowed');
        expect((await flowAccess(true)).clusterWide).toBe('denied');
    });
});
