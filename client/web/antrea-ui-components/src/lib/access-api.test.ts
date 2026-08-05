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
    resetAccessSummary,
    can,
    canNonResource,
    accessibleNamespaces,
    canViewSummary,
    GATE_CONTROLLER_INFO_GET,
    type AccessSummary,
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
