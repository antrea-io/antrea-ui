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

import { afterEach, describe, expect, test, vi } from 'vitest';
import {
    accessSummary,
    namespaceAccessSummaries,
    MAX_NAMESPACE_ACCESS_NAMES,
    resetAccessSummary,
    can,
    verdict,
    canNonResource,
    accessibleNamespaces,
    canViewSummary,
    GATE_CONTROLLER_INFO_GET,
    type AccessSummary,
    type NamespaceAccessSummaryList,
    type SubjectRules,
} from './access-api';
import { APIError, setApiBase } from './api';

function jsonResponse(body: unknown, status = 200): Response {
    return new Response(JSON.stringify(body), { status });
}

function rules(overrides: Partial<SubjectRules> = {}): SubjectRules {
    return {
        resourceRules: [],
        nonResourceRules: [],
        incomplete: false,
        ...overrides,
    };
}

function summary(overrides: Partial<AccessSummary> = {}): AccessSummary {
    return {
        username: 'alice',
        groups: [],
        clusterAdmin: false,
        rules: rules(),
        namespaces: [],
        ...overrides,
    };
}

afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
    setApiBase('');
    resetAccessSummary();
});

describe('accessSummary', () => {
    test('fetches GET /api/v1/access-summary', async () => {
        const fetchMock = vi.fn().mockResolvedValue(jsonResponse(summary()));
        vi.stubGlobal('fetch', fetchMock);

        const s = await accessSummary();
        expect(s.username).toBe('alice');
        expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/access-summary');
    });

    test('memoizes: a second call does not re-fetch', async () => {
        const fetchMock = vi.fn().mockResolvedValue(jsonResponse(summary()));
        vi.stubGlobal('fetch', fetchMock);

        await accessSummary();
        await accessSummary();
        expect(fetchMock).toHaveBeenCalledTimes(1);
    });

    test('resetAccessSummary() clears the memo so the next call re-fetches', async () => {
        const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(jsonResponse(summary())));
        vi.stubGlobal('fetch', fetchMock);

        await accessSummary();
        resetAccessSummary();
        await accessSummary();
        expect(fetchMock).toHaveBeenCalledTimes(2);
    });

    test('does not memoize a rejection: the next call retries', async () => {
        const fetchMock = vi.fn()
            .mockResolvedValueOnce(jsonResponse({ message: 'boom' }, 500))
            .mockResolvedValue(jsonResponse(summary()));
        vi.stubGlobal('fetch', fetchMock);

        await expect(accessSummary()).rejects.toThrow();
        // Without the retry, one transient failure would leave every gate failing open for the
        // rest of the session, silently.
        const s = await accessSummary();
        expect(s.username).toBe('alice');
        expect(fetchMock).toHaveBeenCalledTimes(2);
    });

    test('aborts a request that never settles, so the shell is not blocked forever', async () => {
        vi.useFakeTimers();
        const fetchMock = vi.fn().mockImplementation((_url: string, init: RequestInit) => (
            // Never resolves on its own: only the abort ends it, like a proxy holding the
            // connection open or a wedged backend.
            new Promise((_resolve, reject) => {
                init.signal?.addEventListener('abort', () => reject(new Error('aborted')));
            })
        ));
        vi.stubGlobal('fetch', fetchMock);

        // Settle the rejection into a value first: advancing the timers is what rejects it, so
        // the handler has to be attached before, or the rejection surfaces as unhandled.
        const outcome = accessSummary().then(() => 'resolved', (err: unknown) => err);
        await vi.advanceTimersByTimeAsync(10_000);
        await expect(outcome).resolves.toBeInstanceOf(APIError);

        // And the abort is not sticky: the memo is cleared like any other failure.
        vi.useRealTimers();
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(summary())));
        expect((await accessSummary()).username).toBe('alice');
    });

    test('does not abort a request that completes in time', async () => {
        vi.useFakeTimers();
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(summary())));

        const s = await accessSummary();
        expect(s.username).toBe('alice');
        // The timer is cleared on settle, so nothing is left pending to fire later.
        expect(vi.getTimerCount()).toBe(0);
    });
});

function namespaceList(overrides: Partial<NamespaceAccessSummaryList> = {}): NamespaceAccessSummaryList {
    return { items: [{ namespace: 'ns-a', evaluationFailed: false, rules: rules() }], ...overrides };
}

describe('namespaceAccessSummaries', () => {
    function requestedUrl(fetchMock: { mock: { calls: unknown[][] } }, call = 0): URL {
        return new URL(String(fetchMock.mock.calls[call][0]), 'http://example.test');
    }

    test('fetches GET /api/v1/access-summary/namespaces naming each namespace', async () => {
        const fetchMock = vi.fn().mockResolvedValue(jsonResponse(namespaceList()));
        vi.stubGlobal('fetch', fetchMock);

        const list = await namespaceAccessSummaries(['ns-b', 'ns-a']);

        expect(list.items.map((i) => i.namespace)).toEqual(['ns-a']);
        const url = requestedUrl(fetchMock);
        expect(url.pathname).toBe('/api/v1/access-summary/namespaces');
        expect(url.searchParams.getAll('namespace')).toEqual(['ns-b', 'ns-a']);
    });

    test('names a namespace once however often it is given', async () => {
        const fetchMock = vi.fn().mockResolvedValue(jsonResponse(namespaceList()));
        vi.stubGlobal('fetch', fetchMock);

        await namespaceAccessSummaries(['ns-a', 'ns-b', 'ns-a']);

        expect(requestedUrl(fetchMock).searchParams.getAll('namespace')).toEqual(['ns-a', 'ns-b']);
    });

    test('asks nothing about no namespaces', async () => {
        const fetchMock = vi.fn();
        vi.stubGlobal('fetch', fetchMock);

        expect(await namespaceAccessSummaries([])).toEqual({ items: [] });
        expect(fetchMock).not.toHaveBeenCalled();
    });

    test('rejects more namespaces than the backend answers for, without asking', async () => {
        const fetchMock = vi.fn();
        vi.stubGlobal('fetch', fetchMock);
        const names = Array.from({ length: MAX_NAMESPACE_ACCESS_NAMES + 1 }, (_, i) => `ns-${i}`);

        await expect(namespaceAccessSummaries(names)).rejects.toBeInstanceOf(RangeError);
        expect(fetchMock).not.toHaveBeenCalled();
    });

    test('memoizes the same question: a second call does not re-fetch', async () => {
        const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(jsonResponse(namespaceList())));
        vi.stubGlobal('fetch', fetchMock);

        await namespaceAccessSummaries(['ns-a', 'ns-b']);
        await namespaceAccessSummaries(['ns-a', 'ns-b']);

        expect(fetchMock).toHaveBeenCalledTimes(1);
    });

    test('another set of namespaces, or the same in another order, is another question', async () => {
        const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(jsonResponse(namespaceList())));
        vi.stubGlobal('fetch', fetchMock);

        await namespaceAccessSummaries(['ns-a', 'ns-b']);
        await namespaceAccessSummaries(['ns-a']);
        await namespaceAccessSummaries(['ns-b', 'ns-a']);

        expect(fetchMock).toHaveBeenCalledTimes(3);
    });

    test('reuses an answer for 30 seconds and then asks again, so a changed grant shows up', async () => {
        vi.useFakeTimers();
        const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(jsonResponse(namespaceList())));
        vi.stubGlobal('fetch', fetchMock);

        await namespaceAccessSummaries(['ns-a']);
        await vi.advanceTimersByTimeAsync(29_999);
        await namespaceAccessSummaries(['ns-a']);
        expect(fetchMock).toHaveBeenCalledTimes(1);

        await vi.advanceTimersByTimeAsync(1);
        await namespaceAccessSummaries(['ns-a']);
        expect(fetchMock).toHaveBeenCalledTimes(2);
    });

    test('the 30 seconds run from when the answer arrived, not from when it was asked for', async () => {
        vi.useFakeTimers();
        let release: () => void = () => {};
        const fetchMock = vi.fn().mockImplementation(() => new Promise<Response>((resolve) => {
            release = () => resolve(jsonResponse(namespaceList()));
        }));
        vi.stubGlobal('fetch', fetchMock);

        const pending = namespaceAccessSummaries(['ns-a']);
        await vi.advanceTimersByTimeAsync(9_000);
        // Still pending: a second caller joins it, however long it has been out.
        const joined = namespaceAccessSummaries(['ns-a']);
        release();
        await Promise.all([pending, joined]);
        expect(fetchMock).toHaveBeenCalledTimes(1);

        await vi.advanceTimersByTimeAsync(29_000);
        await namespaceAccessSummaries(['ns-a']);
        expect(fetchMock).toHaveBeenCalledTimes(1);
    });

    test('is a different memo from the cluster-scoped summary', async () => {
        const fetchMock = vi.fn().mockImplementation((url: string) => Promise.resolve(
            jsonResponse(String(url).includes('/namespaces') ? namespaceList() : summary())));
        vi.stubGlobal('fetch', fetchMock);

        await accessSummary();
        await namespaceAccessSummaries(['ns-a']);

        expect(fetchMock).toHaveBeenCalledTimes(2);
    });

    test('resetAccessSummary() clears the memo, so one session never sees the last one\'s answer', async () => {
        const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(jsonResponse(namespaceList())));
        vi.stubGlobal('fetch', fetchMock);

        await namespaceAccessSummaries(['ns-a']);
        resetAccessSummary();
        await namespaceAccessSummaries(['ns-a']);

        expect(fetchMock).toHaveBeenCalledTimes(2);
    });

    test('does not memoize a rejection: the next call retries', async () => {
        const fetchMock = vi.fn()
            .mockResolvedValueOnce(jsonResponse({ message: 'boom' }, 503))
            .mockResolvedValueOnce(jsonResponse(namespaceList()));
        vi.stubGlobal('fetch', fetchMock);

        await expect(namespaceAccessSummaries(['ns-a'])).rejects.toBeInstanceOf(APIError);
        expect((await namespaceAccessSummaries(['ns-a'])).items).toHaveLength(1);
        expect(fetchMock).toHaveBeenCalledTimes(2);
    });

    test('does not memoize an answer in which a review failed: the next call asks again', async () => {
        const failed = namespaceList({
            items: [{ namespace: 'ns-a', evaluationFailed: true, rules: rules({ incomplete: true }) }],
        });
        const fetchMock = vi.fn()
            .mockResolvedValueOnce(jsonResponse(failed))
            .mockResolvedValueOnce(jsonResponse(namespaceList()));
        vi.stubGlobal('fetch', fetchMock);

        expect((await namespaceAccessSummaries(['ns-a'])).items[0].evaluationFailed).toBe(true);
        expect((await namespaceAccessSummaries(['ns-a'])).items[0].evaluationFailed).toBe(false);
        expect(fetchMock).toHaveBeenCalledTimes(2);
        await namespaceAccessSummaries(['ns-a']);
        expect(fetchMock).toHaveBeenCalledTimes(2);
    });

    test('forgets the whole answer when only some of its reviews failed', async () => {
        const grantFlows = { apiGroups: ['observability.antrea.io'], resources: ['flows'], verbs: ['watch'] };
        const mixed = namespaceList({
            items: [
                { namespace: 'ns-a', evaluationFailed: false, rules: rules({ resourceRules: [grantFlows] }) },
                { namespace: 'ns-b', evaluationFailed: true, rules: rules({ incomplete: true }) },
            ],
        });
        const complete = namespaceList({
            items: [
                { namespace: 'ns-a', evaluationFailed: false, rules: rules({ resourceRules: [grantFlows] }) },
                { namespace: 'ns-b', evaluationFailed: false, rules: rules() },
            ],
        });
        const fetchMock = vi.fn()
            .mockResolvedValueOnce(jsonResponse(mixed))
            .mockResolvedValueOnce(jsonResponse(complete));
        vi.stubGlobal('fetch', fetchMock);

        expect((await namespaceAccessSummaries(['ns-a', 'ns-b'])).items[1].evaluationFailed).toBe(true);
        // The item which succeeded is not served from the memo either: the next call asks for both.
        expect((await namespaceAccessSummaries(['ns-a', 'ns-b'])).items[1].evaluationFailed).toBe(false);
        expect(fetchMock).toHaveBeenCalledTimes(2);
        await namespaceAccessSummaries(['ns-a', 'ns-b']);
        expect(fetchMock).toHaveBeenCalledTimes(2);
    });

    test('aborts a request that never settles, like the cluster-scoped summary', async () => {
        vi.useFakeTimers();
        vi.stubGlobal('fetch', vi.fn().mockImplementation((_url: string, init: RequestInit) => (
            new Promise((_resolve, reject) => {
                init.signal?.addEventListener('abort', () => reject(new Error('aborted')));
            })
        )));

        const outcome = namespaceAccessSummaries(['ns-a']).then(() => 'resolved', (err: unknown) => err);
        await vi.advanceTimersByTimeAsync(10_000);
        await expect(outcome).resolves.toBeInstanceOf(APIError);
    });
});

describe('verdict', () => {
    const query = { group: 'observability.antrea.io', resource: 'flows', verb: 'watch' };
    const grant = { apiGroups: ['observability.antrea.io'], resources: ['flows'], verbs: ['watch'] };

    test('allowed when a rule matches', () => {
        expect(verdict(summary({ rules: rules({ resourceRules: [grant] }) }), query)).toBe('allowed');
    });

    test('allowed when a rule matches even if the list is incomplete', () => {
        // Rules are additive: a rule that is there is a grant, whatever else is missing.
        expect(verdict({ rules: rules({ resourceRules: [grant], incomplete: true }) }, query)).toBe('allowed');
    });

    test('unknown when nothing matches in an incomplete list', () => {
        expect(verdict({ rules: rules({ incomplete: true, evaluationError: 'boom' }) }, query)).toBe('unknown');
    });

    test('denied when nothing matches in an exhaustive list', () => {
        expect(verdict({ rules: rules() }, query)).toBe('denied');
        expect(verdict({ rules: rules({ resourceRules: [{ ...grant, verbs: ['list'] }] }) }, query)).toBe('denied');
    });

    test('unknown for null', () => {
        expect(verdict(null, query)).toBe('unknown');
    });

    test('unknown for an item whose review failed', () => {
        // What the backend reports for a Namespace it could not evaluate: no rules, incomplete.
        const failed = { namespace: 'ns-a', evaluationFailed: true, rules: rules({ incomplete: true }) };
        expect(verdict(failed, query)).toBe('unknown');
        expect(can(failed, query)).toBe(true);
    });

    test('applies to one entry of the per-namespace list', () => {
        const list = namespaceList({
            items: [
                { namespace: 'ns-a', evaluationFailed: false, rules: rules({ resourceRules: [grant] }) },
                { namespace: 'ns-b', evaluationFailed: false, rules: rules() },
                { namespace: 'ns-c', evaluationFailed: false, rules: rules({ incomplete: true }) },
            ],
        });
        expect(list.items.map((i) => verdict(i, query))).toEqual(['allowed', 'denied', 'unknown']);
    });

    test('can() is verdict() with unknown allowed', () => {
        expect(can({ rules: rules({ incomplete: true }) }, query)).toBe(true);
        expect(can({ rules: rules() }, query)).toBe(false);
    });
});

describe('can', () => {
    test('fails open when summary is null', () => {
        expect(can(null, { group: 'crd.antrea.io', resource: 'traceflows', verb: 'create' })).toBe(true);
    });

    test('fails open when rules are incomplete', () => {
        const s = summary({ rules: rules({ incomplete: true }) });
        expect(can(s, { group: 'crd.antrea.io', resource: 'traceflows', verb: 'create' })).toBe(true);
    });

    test('matches an exact rule', () => {
        const s = summary({
            rules: rules({
                resourceRules: [{ apiGroups: ['crd.antrea.io'], resources: ['traceflows'], verbs: ['create'] }],
            }),
        });
        expect(can(s, { group: 'crd.antrea.io', resource: 'traceflows', verb: 'create' })).toBe(true);
        expect(can(s, { group: 'crd.antrea.io', resource: 'traceflows', verb: 'delete' })).toBe(false);
    });

    test('matches a wildcard in any of the three fields', () => {
        const wildGroup = summary({ rules: rules({ resourceRules: [{ apiGroups: ['*'], resources: ['traceflows'], verbs: ['create'] }] }) });
        expect(can(wildGroup, { group: 'crd.antrea.io', resource: 'traceflows', verb: 'create' })).toBe(true);

        const wildResource = summary({ rules: rules({ resourceRules: [{ apiGroups: ['crd.antrea.io'], resources: ['*'], verbs: ['create'] }] }) });
        expect(can(wildResource, { group: 'crd.antrea.io', resource: 'traceflows', verb: 'create' })).toBe(true);

        const wildVerb = summary({ rules: rules({ resourceRules: [{ apiGroups: ['crd.antrea.io'], resources: ['traceflows'], verbs: ['*'] }] }) });
        expect(can(wildVerb, { group: 'crd.antrea.io', resource: 'traceflows', verb: 'create' })).toBe(true);
    });

    test('the core API group is the empty string, not "core"', () => {
        const s = summary({ rules: rules({ resourceRules: [{ apiGroups: [''], resources: ['pods'], verbs: ['get'] }] }) });
        expect(can(s, { group: '', resource: 'pods', verb: 'get' })).toBe(true);
        expect(can(s, { group: 'core', resource: 'pods', verb: 'get' })).toBe(false);
    });

    test('subresources are matched as the literal string', () => {
        const s = summary({ rules: rules({ resourceRules: [{ apiGroups: ['crd.antrea.io'], resources: ['traceflows/status'], verbs: ['get'] }] }) });
        expect(can(s, { group: 'crd.antrea.io', resource: 'traceflows/status', verb: 'get' })).toBe(true);
        expect(can(s, { group: 'crd.antrea.io', resource: 'traceflows', verb: 'get' })).toBe(false);
    });

    test('null rule lists deny rather than throw', () => {
        // SubjectRulesReviewStatus marshals empty rule lists as null.
        const s = { ...summary(), rules: { incomplete: false, resourceRules: null, nonResourceRules: null } } as unknown as AccessSummary;
        expect(can(s, { group: 'crd.antrea.io', resource: 'traceflows', verb: 'create' })).toBe(false);
        expect(canNonResource(s, { verb: 'get', url: '/featuregates' })).toBe(false);
    });

    test('a rule with resourceNames grants nothing generally', () => {
        const s = summary({
            rules: rules({
                resourceRules: [{ apiGroups: [''], resources: ['configmaps'], verbs: ['get'], resourceNames: ['antrea-config'] }],
            }),
        });
        expect(can(s, { group: '', resource: 'configmaps', verb: 'get' })).toBe(false);
        expect(can(s, { group: '', resource: 'configmaps', verb: 'get', name: 'antrea-config' })).toBe(true);
        expect(can(s, { group: '', resource: 'configmaps', verb: 'get', name: 'other' })).toBe(false);
    });

    test('the controller gate names the object it fetches, so a resourceNames grant matches', () => {
        // A least-privilege narrowing of antrea-ui-admin-core: the summary page only ever GETs
        // antreacontrollerinfos/antrea-controller, and this rule does authorize that.
        const s = summary({
            rules: rules({
                resourceRules: [{
                    apiGroups: ['crd.antrea.io'],
                    resources: ['antreacontrollerinfos'],
                    verbs: ['get'],
                    resourceNames: ['antrea-controller'],
                }],
            }),
        });
        expect(can(s, GATE_CONTROLLER_INFO_GET)).toBe(true);
        expect(canViewSummary(s)).toBe(true);
    });
});

describe('canNonResource', () => {
    test('fails open when summary is null or incomplete', () => {
        expect(canNonResource(null, { verb: 'get', url: '/featuregates' })).toBe(true);
        expect(canNonResource(summary({ rules: rules({ incomplete: true }) }), { verb: 'get', url: '/featuregates' })).toBe(true);
    });

    test('matches nonResourceURLs', () => {
        const s = summary({ rules: rules({ nonResourceRules: [{ verbs: ['get'], nonResourceURLs: ['/featuregates'] }] }) });
        expect(canNonResource(s, { verb: 'get', url: '/featuregates' })).toBe(true);
        expect(canNonResource(s, { verb: 'get', url: '/other' })).toBe(false);
    });

    test('matches a trailing-* prefix, like the RBAC authorizer', () => {
        // nonResourceURLs: ["/*"] is the common way to grant every non-resource endpoint.
        const all = summary({ rules: rules({ nonResourceRules: [{ verbs: ['get'], nonResourceURLs: ['/*'] }] }) });
        expect(canNonResource(all, { verb: 'get', url: '/featuregates' })).toBe(true);

        const prefix = summary({ rules: rules({ nonResourceRules: [{ verbs: ['get'], nonResourceURLs: ['/feature*'] }] }) });
        expect(canNonResource(prefix, { verb: 'get', url: '/featuregates' })).toBe(true);
        expect(canNonResource(prefix, { verb: 'get', url: '/healthz' })).toBe(false);

        const bare = summary({ rules: rules({ nonResourceRules: [{ verbs: ['get'], nonResourceURLs: ['*'] }] }) });
        expect(canNonResource(bare, { verb: 'get', url: '/featuregates' })).toBe(true);
    });

    test('a * that is not trailing is a literal, not a wildcard', () => {
        // Matches the authorizer, which only ever checks HasSuffix("*").
        const s = summary({ rules: rules({ nonResourceRules: [{ verbs: ['get'], nonResourceURLs: ['/feature*gates'] }] }) });
        expect(canNonResource(s, { verb: 'get', url: '/featuregates' })).toBe(false);
        expect(canNonResource(s, { verb: 'get', url: '/feature*gates' })).toBe(true);
    });

    test('the verb is still matched exactly, prefixes do not apply to it', () => {
        const s = summary({ rules: rules({ nonResourceRules: [{ verbs: ['get'], nonResourceURLs: ['/*'] }] }) });
        expect(canNonResource(s, { verb: 'post', url: '/featuregates' })).toBe(false);
    });
});

describe('accessibleNamespaces', () => {
    test('null for null summary', () => {
        expect(accessibleNamespaces(null)).toBeNull();
    });

    test('null for incomplete rules', () => {
        expect(accessibleNamespaces(summary({ rules: rules({ incomplete: true }) }))).toBeNull();
    });

    test('null for ["*"]', () => {
        expect(accessibleNamespaces(summary({ namespaces: ['*'] }))).toBeNull();
    });

    test('the concrete list otherwise', () => {
        expect(accessibleNamespaces(summary({ namespaces: ['ns-a', 'ns-b'] }))).toEqual(['ns-a', 'ns-b']);
    });

    test('null, not a throw, when namespaces is null', () => {
        // An older server sends "namespaces": null when it could not resolve the list, and
        // does not set rules.incomplete for it, so the early return above does not cover this.
        const s = { ...summary(), namespaces: null } as unknown as AccessSummary;
        expect(accessibleNamespaces(s)).toBeNull();
    });
});

describe('canViewSummary', () => {
    test('true if any of the three summary-card gates is granted', () => {
        const agentInfo = summary({ rules: rules({ resourceRules: [{ apiGroups: ['crd.antrea.io'], resources: ['antreaagentinfos'], verbs: ['list'] }] }) });
        expect(canViewSummary(agentInfo)).toBe(true);

        const controllerInfo = summary({ rules: rules({ resourceRules: [{ apiGroups: ['crd.antrea.io'], resources: ['antreacontrollerinfos'], verbs: ['get'] }] }) });
        expect(canViewSummary(controllerInfo)).toBe(true);

        const featureGates = summary({ rules: rules({ nonResourceRules: [{ verbs: ['get'], nonResourceURLs: ['/featuregates'] }] }) });
        expect(canViewSummary(featureGates)).toBe(true);

        const none = summary();
        expect(canViewSummary(none)).toBe(false);
    });
});

