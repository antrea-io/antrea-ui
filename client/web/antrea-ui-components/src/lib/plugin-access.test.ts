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
import type { AccessSummary } from './access-api';
import { createPluginHostAccess, isNamespaceAware, meetsAccessRequirements, parseAccessRequirements } from './plugin-access';

const PODS_LIST = { group: '', resource: 'pods', verb: 'list' };
const SERVICES_LIST = { group: '', resource: 'services', verb: 'list' };

function summary(overrides: Partial<AccessSummary> = {}): AccessSummary {
    return {
        username: 'alice', groups: [], clusterAdmin: false, namespaces: ['team-a'],
        rules: { resourceRules: [{ verbs: ['list'], apiGroups: [''], resources: ['pods'] }], nonResourceRules: [], incomplete: false },
        ...overrides,
    };
}

afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
});

describe('parseAccessRequirements', () => {
    test('no requirement means ungated', () => {
        expect(parseAccessRequirements(undefined, 'x')).toEqual([]);
        expect(parseAccessRequirements(null, 'x')).toEqual([]);
    });

    test.each([
        ['namespaceScope alone', { namespaceScope: true }],
        ['namespaceScope with a plain array', { namespaceScope: true, resources: [PODS_LIST] }],
        ['namespaceScope with anyOf', { namespaceScope: true, resources: { anyOf: [PODS_LIST, SERVICES_LIST] } }],
        ['allOf without namespaceScope', { resources: { allOf: [PODS_LIST] } }],
        ['anyOf without namespaceScope', { resources: { anyOf: [PODS_LIST] } }],
        ['clusterAdmin beside the other fields', { clusterAdmin: true, namespaceScope: true, resources: [PODS_LIST] }],
        ['both flags false', { clusterAdmin: false, namespaceScope: false }],
    ])('%s is kept as-is', (_name, requires) => {
        expect(parseAccessRequirements(requires, 'x')).toEqual([requires]);
    });

    test('a well-formed requirement is kept as-is', () => {
        const requirement = { clusterAdmin: true, resources: [{ ...PODS_LIST, name: 'p' }] };
        expect(parseAccessRequirements(requirement, 'x')).toEqual([requirement]);
    });

    test.each([
        ['a string', 'clusterAdmin'],
        ['an unknown requirement key', { clusterAdmn: true }],
        ['a misspelled requirement beside a valid one', { clusterAdmn: true, resources: [PODS_LIST] }],
        ['an unknown resource query key', { resources: [{ ...PODS_LIST, names: ['p'] }] }],
        ['an array', [{ clusterAdmin: true }]],
        ['a non-boolean clusterAdmin', { clusterAdmin: 'true' }],
        ['a non-array resources', { resources: PODS_LIST }],
        ['a query missing its verb', { resources: [{ group: '', resource: 'pods' }] }],
        ['a query with an empty resource', { resources: [{ group: '', resource: '', verb: 'list' }] }],
        ['a non-boolean namespaceScope', { namespaceScope: 'true' }],
        ['an empty resources array', { resources: [] }],
        ['an empty allOf', { resources: { allOf: [] } }],
        ['an empty anyOf', { resources: { anyOf: [] } }],
        ['an empty resources object', { resources: {} }],
        ['allOf and anyOf together', { resources: { allOf: [PODS_LIST], anyOf: [SERVICES_LIST] } }],
        ['an unknown resources key', { resources: { oneOf: [PODS_LIST] } }],
        ['an anyOf query missing its verb', { resources: { anyOf: [{ group: '', resource: 'pods' }] } }],
        ['allOf with namespaceScope', { namespaceScope: true, resources: { allOf: [PODS_LIST] } }],
    ])('%s is logged and denies', (_name, requires) => {
        const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
        const requirements = parseAccessRequirements(requires, 'sidebar entry "/x"');

        expect(errorSpy).toHaveBeenCalledWith(expect.stringContaining('sidebar entry "/x" has a malformed "requires"'));
        expect(meetsAccessRequirements(summary({ clusterAdmin: true }), requirements)).toBe(false);
        expect(meetsAccessRequirements(null, requirements)).toBe(false);
    });
});

describe('meetsAccessRequirements', () => {
    test('clusterAdmin passes only for cluster admins', () => {
        expect(meetsAccessRequirements(summary(), [{ clusterAdmin: true }])).toBe(false);
        expect(meetsAccessRequirements(summary({ clusterAdmin: true }), [{ clusterAdmin: true }])).toBe(true);
    });

    test('every resource query must be allowed', () => {
        expect(meetsAccessRequirements(summary(), [{ resources: [PODS_LIST] }])).toBe(true);
        expect(meetsAccessRequirements(summary(), [{ resources: [PODS_LIST, { group: '', resource: 'services', verb: 'list' }] }])).toBe(false);
    });

    test('every requirement in the list must hold', () => {
        expect(meetsAccessRequirements(summary(), [{ resources: [PODS_LIST] }, { clusterAdmin: true }])).toBe(false);
    });

    test('incomplete rules leave an unmatched query allowed, like can()', () => {
        const s = summary({ rules: { resourceRules: [], nonResourceRules: [], incomplete: true } });
        expect(meetsAccessRequirements(s, [{ resources: [{ group: '', resource: 'secrets', verb: 'list' }] }])).toBe(true);
    });

    test('a failed summary fetch fails open', () => {
        expect(meetsAccessRequirements(null, [{ clusterAdmin: true, resources: [PODS_LIST] }])).toBe(true);
    });

    test('no requirements always pass', () => {
        expect(meetsAccessRequirements(summary(), [])).toBe(true);
        expect(meetsAccessRequirements(summary(), undefined)).toBe(true);
        expect(meetsAccessRequirements(summary(), [{}])).toBe(true);
    });
});

const noBinding = (overrides: Partial<AccessSummary> = {}) => summary({
    namespaces: [], rules: { resourceRules: [], nonResourceRules: [], incomplete: false }, ...overrides,
});

describe('meetsAccessRequirements: resources forms', () => {
    test('anyOf needs one allowed query, allOf and a plain array need every one', () => {
        expect(meetsAccessRequirements(summary(), [{ resources: { anyOf: [SERVICES_LIST, PODS_LIST] } }])).toBe(true);
        expect(meetsAccessRequirements(summary(), [{ resources: { anyOf: [SERVICES_LIST] } }])).toBe(false);
        expect(meetsAccessRequirements(summary(), [{ resources: { allOf: [PODS_LIST, SERVICES_LIST] } }])).toBe(false);
        expect(meetsAccessRequirements(summary(), [{ resources: { allOf: [PODS_LIST] } }])).toBe(true);
    });
});

describe('meetsAccessRequirements: clusterAdmin decides alone', () => {
    test('other fields are ignored when clusterAdmin is true', () => {
        const requirement = [{ clusterAdmin: true, namespaceScope: true, resources: [PODS_LIST] }];
        expect(meetsAccessRequirements(summary(), requirement)).toBe(false);
        expect(meetsAccessRequirements(summary({ clusterAdmin: true }), requirement)).toBe(true);
    });

    test('clusterAdmin: false adds no condition', () => {
        expect(meetsAccessRequirements(noBinding(), [{ clusterAdmin: false }])).toBe(true);
        expect(meetsAccessRequirements(noBinding(), [{ clusterAdmin: false, resources: [PODS_LIST] }])).toBe(false);
    });
});

describe('meetsAccessRequirements: namespaceScope', () => {
    test('passes when the page verdict is allowed', () => {
        const s = noBinding({ rules: { resourceRules: [{ verbs: ['list'], apiGroups: [''], resources: ['pods'] }], nonResourceRules: [], incomplete: false } });
        expect(meetsAccessRequirements(s, [{ namespaceScope: true, resources: [PODS_LIST] }])).toBe(true);
    });

    test('passes for a user named in a RoleBinding even if the queries are denied', () => {
        const s = summary({ namespaces: ['team-a'] });
        expect(meetsAccessRequirements(s, [{ namespaceScope: true, resources: [SERVICES_LIST] }])).toBe(true);
    });

    test('passes for a user who may list Namespaces', () => {
        const s = summary({
            namespaces: ['*'],
            rules: { resourceRules: [{ verbs: ['list'], apiGroups: [''], resources: ['namespaces'] }], nonResourceRules: [], incomplete: false },
        });
        expect(meetsAccessRequirements(s, [{ namespaceScope: true, resources: [SERVICES_LIST] }])).toBe(true);
    });

    test('hides from a user with no grant, no binding and no way to list Namespaces', () => {
        expect(meetsAccessRequirements(noBinding(), [{ namespaceScope: true, resources: [SERVICES_LIST] }])).toBe(false);
    });

    test('any allowed query makes the verdict allowed', () => {
        const s = noBinding({ rules: { resourceRules: [{ verbs: ['list'], apiGroups: [''], resources: ['pods'] }], nonResourceRules: [], incomplete: false } });
        expect(meetsAccessRequirements(s, [{ namespaceScope: true, resources: { anyOf: [SERVICES_LIST, PODS_LIST] } }])).toBe(true);
    });

    test('an unknown verdict keeps the item available', () => {
        const s = noBinding({ rules: { resourceRules: [], nonResourceRules: [], incomplete: true } });
        expect(meetsAccessRequirements(s, [{ namespaceScope: true, resources: [SERVICES_LIST] }])).toBe(true);
    });

    test('without resources the verdict is unknown for a non-admin, so everyone sees it', () => {
        expect(meetsAccessRequirements(noBinding(), [{ namespaceScope: true }])).toBe(true);
        expect(meetsAccessRequirements(noBinding({ clusterAdmin: true }), [{ namespaceScope: true }])).toBe(true);
    });

    test('namespaceScope: false adds no widening', () => {
        expect(meetsAccessRequirements(summary(), [{ namespaceScope: false, resources: [SERVICES_LIST] }])).toBe(false);
    });

    test('a failed summary fetch fails open', () => {
        expect(meetsAccessRequirements(null, [{ namespaceScope: true, resources: [SERVICES_LIST] }])).toBe(true);
    });
});

describe('isNamespaceAware', () => {
    test('is true only for namespaceScope without clusterAdmin', () => {
        expect(isNamespaceAware([{ namespaceScope: true }])).toBe(true);
        expect(isNamespaceAware([{ namespaceScope: true, resources: [PODS_LIST] }])).toBe(true);
        expect(isNamespaceAware([{ namespaceScope: true, clusterAdmin: true }])).toBe(false);
        expect(isNamespaceAware([{ namespaceScope: false }])).toBe(false);
        expect(isNamespaceAware([{ resources: [PODS_LIST] }])).toBe(false);
        expect(isNamespaceAware(undefined)).toBe(false);
    });

    test('a malformed requirement is not namespace-aware', () => {
        vi.spyOn(console, 'error').mockImplementation(() => {});
        expect(isNamespaceAware(parseAccessRequirements({ namespaceScope: 'yes' }, 'x'))).toBe(false);
    });
});

describe('createPluginHostAccess', () => {
    test('before the host has published, nothing is known and ready() waits', async () => {
        const { access, publish } = createPluginHostAccess();
        let resolved: AccessSummary | null | undefined;
        void access.ready().then(s => { resolved = s; });
        await Promise.resolve();

        expect(access.loaded()).toBe(false);
        expect(access.summary()).toBeNull();
        expect(resolved).toBeUndefined();

        const s = summary();
        publish(true, s);
        await Promise.resolve();
        expect(resolved).toBe(s);
        await expect(access.ready()).resolves.toBe(s);
    });

    test('answers from the published summary', () => {
        const { access, publish } = createPluginHostAccess();
        publish(true, summary());

        expect(access.can(PODS_LIST)).toBe(true);
        expect(access.can({ group: '', resource: 'secrets', verb: 'list' })).toBe(false);
        expect(access.verdict({ group: '', resource: 'secrets', verb: 'list' })).toBe('denied');
        expect(access.isClusterAdmin()).toBe(false);
        expect(access.accessibleNamespaces()).toEqual(['team-a']);
        expect(access.meets({ resources: [PODS_LIST] })).toBe(true);
        expect(access.meets({ clusterAdmin: true })).toBe(false);

        publish(true, summary({ clusterAdmin: true }));
        expect(access.isClusterAdmin()).toBe(true);
        expect(access.meets({ clusterAdmin: true })).toBe(true);
    });

    test('meets() denies a malformed requirement', () => {
        vi.spyOn(console, 'error').mockImplementation(() => {});
        const { access, publish } = createPluginHostAccess();
        publish(true, summary({ clusterAdmin: true }));

        expect(access.meets({ clusterAdmin: 'yes' } as never)).toBe(false);
    });

    test('subscribers hear about changes until they unsubscribe', () => {
        const { access, publish } = createPluginHostAccess();
        const listener = vi.fn();
        const unsubscribe = access.subscribe(listener);
        const s = summary();

        publish(true, s);
        publish(true, s); // no change, no call
        expect(listener).toHaveBeenCalledExactlyOnceWith(s);

        unsubscribe();
        publish(false, null);
        expect(listener).toHaveBeenCalledOnce();
    });

    test('a throwing subscriber does not stop the others', () => {
        vi.spyOn(console, 'error').mockImplementation(() => {});
        const { access, publish } = createPluginHostAccess();
        const second = vi.fn();
        access.subscribe(() => { throw new Error('boom'); });
        access.subscribe(second);

        publish(true, summary());
        expect(second).toHaveBeenCalledOnce();
    });

    test('forNamespace() returns the rules for that Namespace, or null on failure', async () => {
        const { access, publish } = createPluginHostAccess();
        publish(true, summary());
        const rules = { resourceRules: [], nonResourceRules: [], incomplete: false };
        const fetchMock = vi.fn(async () => new Response(JSON.stringify({ items: [{ namespace: 'team-a', rules, evaluationFailed: false }] })));
        vi.stubGlobal('fetch', fetchMock);

        await expect(access.forNamespace('team-a')).resolves.toEqual({ namespace: 'team-a', rules, evaluationFailed: false });
        expect(String((fetchMock.mock.calls[0] as unknown[])[0])).toContain('/api/v1/access-summary/namespaces?namespace=team-a');

        vi.spyOn(console, 'error').mockImplementation(() => {});
        vi.stubGlobal('fetch', vi.fn(async () => new Response('', { status: 500 })));
        await expect(access.forNamespace('team-b')).resolves.toBeNull();
    });
});
