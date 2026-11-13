/**
 * Copyright 2026 Antrea Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import { loadPlugins, isCompatible, isValidRange, getIncompatiblePlugins, incompatiblePluginMessage, dedupeByPath, getPluginRoutes, getPluginSidebarEntries, resolveParentPaths, getLandingPageTabs, allowsPluginItem, publishPluginAccess, type PluginManifest, type PluginRoute, type PluginSidebarEntry, type LandingPageTab } from './plugins';
import type { AccessSummary } from '@antrea/ui-components';

// The shell version loadPlugins() matches compatibleAntreaUI against; a prerelease version, to
// exercise the prerelease semantics a range has to get right.
vi.mock('./config', () => ({ default: { apiServer: '', shellVersion: '1.0.0-rc.2' } }));

import rangeCases from './testdata/compatible_antrea_ui.json';

function jsonResponse(body: unknown, status = 200): Response {
    return new Response(JSON.stringify(body), { status });
}

function manifest(overrides: Partial<PluginManifest> = {}): PluginManifest {
    return { name: 'pod-counter', version: '0.1.0', entry: 'index.js', compatibleAntreaUI: '1.0.0-rc.1 || 1.0.0-rc.2', ...overrides };
}

afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
});

describe('loadPlugins', () => {
    test('index.json fetch failure returns no plugins', async () => {
        vi.stubGlobal('fetch', vi.fn(async () => new Response('nope', { status: 500 })));
        await expect(loadPlugins()).resolves.toEqual([]);
    });

    test('a manifest whose module fails to import() is skipped, others are unaffected', async () => {
        vi.stubGlobal('fetch', vi.fn(async () => jsonResponse([
            manifest({ name: 'broken' }),
        ])));
        const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});

        const loaded = await loadPlugins();

        expect(loaded).toEqual([]);
        expect(errorSpy).toHaveBeenCalledWith(
            expect.stringContaining('failed to load plugin "broken"'),
            expect.anything()
        );
    });
});

describe('loadPlugins compatibility', () => {
    test('a plugin whose range does not admit the shell version is skipped before its code is fetched', async () => {
        vi.stubGlobal('fetch', vi.fn(async () => jsonResponse([
            manifest({ name: 'too-old', version: '2.0.0', compatibleAntreaUI: '1.0.0-rc.1' }),
        ])));
        const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});

        await expect(loadPlugins()).resolves.toEqual([]);

        // The only error logged is the incompatibility message, not "failed to load plugin", so
        // import() was never tried.
        expect(errorSpy).toHaveBeenCalledTimes(1);
        const info = getIncompatiblePlugins().find(p => p.name === 'too-old')!;
        expect(info).toEqual({ name: 'too-old', version: '2.0.0', range: '1.0.0-rc.1', shellVersion: '1.0.0-rc.2' });
        expect(incompatiblePluginMessage(info)).toBe(
            'Plugin too-old (version 2.0.0) does not support antrea-ui version 1.0.0-rc.2. ' +
            'Supported versions: 1.0.0-rc.1. Install a supported shell/plugin combination.'
        );
    });
});

describe('loadPlugins with an unusable shell version or range', () => {
    test('a manifest with no range is reported as having none declared, not as "undefined"', async () => {
        const noRange: Partial<PluginManifest> = manifest({ name: 'old-backend' });
        delete noRange.compatibleAntreaUI;
        vi.stubGlobal('fetch', vi.fn(async () => jsonResponse([noRange])));
        vi.spyOn(console, 'error').mockImplementation(() => {});

        await loadPlugins();

        expect(getIncompatiblePlugins().find(p => p.name === 'old-backend')?.range).toBe('(none declared)');
    });

    test('a shell version that is not valid semver skips the check instead of dropping every plugin', async () => {
        const config = (await import('./config')).default;
        const original = config.shellVersion;
        config.shellVersion = '0.8.0.1';
        vi.stubGlobal('fetch', vi.fn(async () => jsonResponse([manifest({ name: 'bad-version-plugin', compatibleAntreaUI: '>=9.0.0' })])));
        const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
        vi.spyOn(console, 'error').mockImplementation(() => {});
        try {
            await loadPlugins();
        } finally {
            config.shellVersion = original;
        }

        expect(warnSpy).toHaveBeenCalledWith(expect.stringContaining('no valid version'));
        expect(getIncompatiblePlugins().some(p => p.name === 'bad-version-plugin')).toBe(false);
    });
});

describe('isValidRange', () => {
    // Every range the backend accepts must parse here too; the backend may reject more.
    test.each(rangeCases.filter(c => c.valid))('$range is valid', ({ range }) => {
        expect(isValidRange(range)).toBe(true);
    });

    test.each(['', '   ', 'not a range', '>='])('%j is not valid', (range) => {
        expect(isValidRange(range)).toBe(false);
    });
});

describe('isCompatible', () => {
    const cases: [string, string, boolean][] = [
        ['1.0.0-rc.2', '1.0.0-rc.1 || 1.0.0-rc.2', true],
        ['1.0.0-rc.2', '>=1.0.0-rc.1 <1.0.0-rc.3', true],
        // A suffix such as -rc.2 makes it a prerelease, which sorts below 1.0.0 and is not admitted by a
        // range that does not mention a prerelease of the same major.minor.patch.
        ['1.0.0-rc.2', '>=1.0.0 <2.0.0', false],
        ['1.0.0-rc.2', '^1.0.0', false],
        ['1.0.0', '1.0.0-rc.1 || 1.0.0-rc.2', false],
        ['1.0.0', '1.0.0-rc.1 || 1.0.0', true],
        // A gap between two supported releases: satisfies() is the only decision.
        ['1.0.0-rc.2', '1.0.0-rc.1 || 1.0.0-rc.3', false],
        ['1.2.3', '>=1.0.0 <2.0.0', true],
        // "x" is not a wildcard in a prerelease identifier.
        ['1.0.0-rc.2', '1.0.0-rc.x', false],
        // Malformed or missing ranges never match.
        ['1.0.0', 'not a range', false],
        ['1.0.0', '', false],
    ];
    test.each(cases)('shell %s against %j is %s', (shell, range, want) => {
        expect(isCompatible(manifest({ compatibleAntreaUI: range }), shell)).toBe(want);
    });

    test('a missing range never matches', () => {
        expect(isCompatible({ name: 'p', version: '1', entry: 'i.js' } as PluginManifest, '1.0.0')).toBe(false);
    });
});

describe('dedupeByPath', () => {
    test('an empty list passes through unchanged', () => {
        expect(dedupeByPath([], 'route')).toEqual([]);
    });

    test.each(['/settings', 'supportbundle', '/supportbundle'])('built-in path %s is reserved', path => {
        const items = [{ path }];
        const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});

        expect(dedupeByPath(items, 'route')).toEqual([]);
        expect(errorSpy).toHaveBeenCalledWith(expect.stringContaining('collides with a built-in route'));
    });

    test('a path already claimed by an earlier item is dropped', () => {
        const items = [{ path: '/plugin/pod-counter' }, { path: '/plugin/pod-counter' }];
        const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});

        expect(dedupeByPath(items, 'route')).toEqual([items[0]]);
        expect(errorSpy).toHaveBeenCalledWith(expect.stringContaining('already claimed by another plugin'));
    });

    test('unique, non-reserved paths are all kept', () => {
        const items = [{ path: '/plugin/pod-counter' }, { path: '/plugin/other' }];

        expect(dedupeByPath(items, 'route')).toEqual(items);
    });
});

describe('resolveParentPaths', () => {
    function entry(overrides: Partial<PluginSidebarEntry> = {}): PluginSidebarEntry {
        return { label: 'Sub Page', path: '/plugin/sub-page', ...overrides };
    }

    test('an entry with no parentPath passes through unchanged', () => {
        expect(resolveParentPaths([entry()])).toEqual([entry()]);
    });

    test.each(['flows', 'supportbundle'])('built-in parent %s is kept and normalized', parentPath => {
        const result = resolveParentPaths([entry({ parentPath: `/${parentPath}` })]);
        expect(result).toEqual([entry({ parentPath })]);
    });

    test('a parentPath matching another top-level plugin entry is kept', () => {
        const parent = entry({ label: 'Parent', path: '/plugin/parent' });
        const child = entry({ parentPath: 'plugin/parent' });

        expect(resolveParentPaths([parent, child])).toEqual([parent, child]);
    });

    test('a parentPath with no matching top-level entry is dropped, with a logged reason', () => {
        const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
        const child = entry({ parentPath: '/no-such-entry' });

        expect(resolveParentPaths([child])).toEqual([entry()]);
        expect(errorSpy).toHaveBeenCalledWith(expect.stringContaining('is not a top-level entry'));
    });

    test('nesting two levels deep is rejected: a parentPath pointing at an already-nested entry is dropped', () => {
        const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
        const parent = entry({ label: 'Parent', path: '/plugin/parent' });
        const child = entry({ label: 'Child', path: '/plugin/child', parentPath: 'plugin/parent' });
        const grandchild = entry({ label: 'Grandchild', path: '/plugin/grandchild', parentPath: 'plugin/child' });

        const result = resolveParentPaths([parent, child, grandchild]);

        expect(result).toEqual([parent, child, entry({ label: 'Grandchild', path: '/plugin/grandchild' })]);
        expect(errorSpy).toHaveBeenCalledWith(expect.stringContaining('is not a top-level entry (or is itself nested)'));
    });
});

describe('getPluginRoutes / window.__antreaPluginHost', () => {
    test('registerRoute makes the route show up in getPluginRoutes', () => {
        window.__antreaPluginHost!.registerRoute({ path: '/plugin/test-registration', tag: 'antrea-plugin-test-registration' } satisfies PluginRoute);

        expect(getPluginRoutes()).toContainEqual({ path: '/plugin/test-registration', tag: 'antrea-plugin-test-registration' });
    });
});

describe('getLandingPageTabs / window.__antreaPluginHost', () => {
    test('registerLandingPageTab makes the tab show up in getLandingPageTabs', () => {
        const tab: LandingPageTab = { id: 'security-test-registration', label: 'Security', tag: 'antrea-security-test-registration' };
        window.__antreaPluginHost!.registerLandingPageTab(tab);

        expect(getLandingPageTabs()).toContainEqual(tab);
    });

    test('a tab id colliding with the built-in Inventory & Events tab is dropped', () => {
        const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
        window.__antreaPluginHost!.registerLandingPageTab({ id: 'traffic-inventory', label: 'Fake Overview', tag: 'antrea-fake-overview' });

        expect(getLandingPageTabs().find(t => t.id === 'traffic-inventory')).toBeUndefined();
        expect(errorSpy).toHaveBeenCalledWith(expect.stringContaining('collides with the built-in Inventory & Events tab'));
    });

    test('a tab id already claimed by an earlier plugin is dropped', () => {
        const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
        window.__antreaPluginHost!.registerLandingPageTab({ id: 'dup-tab', label: 'First', tag: 'antrea-first' });
        window.__antreaPluginHost!.registerLandingPageTab({ id: 'dup-tab', label: 'Second', tag: 'antrea-second' });

        expect(getLandingPageTabs().filter(t => t.id === 'dup-tab')).toEqual([{ id: 'dup-tab', label: 'First', tag: 'antrea-first' }]);
        expect(errorSpy).toHaveBeenCalledWith(expect.stringContaining('already claimed by another plugin'));
    });
});

describe('permission requirements', () => {
    const admin = (clusterAdmin: boolean): AccessSummary => ({
        username: 'alice', groups: [], clusterAdmin, namespaces: [],
        rules: { resourceRules: [], nonResourceRules: [], incomplete: false },
    });

    test('a registration keeps its validated `requires` as `access`', () => {
        window.__antreaPluginHost!.registerRoute({ path: '/gated-route', tag: 'gated', requires: { clusterAdmin: true } });
        window.__antreaPluginHost!.registerLandingPageTab({ id: 'gated-tab', label: 'Gated', tag: 'gated', requires: { clusterAdmin: true } });

        expect(getPluginRoutes().find(route => route.path === '/gated-route')?.access).toEqual([{ clusterAdmin: true }]);
        expect(getLandingPageTabs().find(tab => tab.id === 'gated-tab')?.access).toEqual([{ clusterAdmin: true }]);
    });

    test('a sidebar entry inherits the requirement of the route at its path', () => {
        window.__antreaPluginHost!.registerRoute({ path: '/gated-page', tag: 'gated', requires: { clusterAdmin: true } });
        window.__antreaPluginHost!.registerSidebarEntry({ label: 'Gated', path: 'gated-page' });

        expect(getPluginSidebarEntries().find(entry => entry.path === 'gated-page')?.access).toEqual([{ clusterAdmin: true }]);
    });

    test('distinct sidebar links inherit route requirements by pathname and keep their own requirements', () => {
        const routeRequirement = { clusterAdmin: true, namespaceScope: true };
        const entryRequirement = { resources: [{ group: '', resource: 'pods', verb: 'list' }] };
        window.__antreaPluginHost!.registerRoute({ path: '/gated-views', tag: 'gated', requires: routeRequirement });
        const paths = [
            '/gated-views?view=a',
            '/gated-views?view=b',
            '/gated-views#details',
            'gated-views?view=a#details',
        ];
        for (const path of paths) {
            window.__antreaPluginHost!.registerSidebarEntry({ label: 'Gated', path, requires: entryRequirement });
        }

        const entries = getPluginSidebarEntries().filter(entry => paths.includes(entry.path));
        const reader = admin(false);
        reader.rules.resourceRules = [{ apiGroups: [''], resources: ['pods'], verbs: ['list'] }];
        expect(entries.map(entry => entry.path)).toEqual(paths);
        for (const entry of entries) {
            expect(entry.access).toEqual([entryRequirement, routeRequirement]);
            expect(allowsPluginItem({ summary: reader, loaded: true }, entry.access)).toBe(false);
            expect(allowsPluginItem({ summary: admin(true), loaded: true }, entry.access)).toBe(true);
        }
    });

    test('an entry is Namespace-aware when its route is, not when only its own requires say so', () => {
        window.__antreaPluginHost!.registerRoute({ path: '/aware-page', tag: 'aware', requires: { clusterAdmin: true, namespaceScope: true } });
        window.__antreaPluginHost!.registerSidebarEntry({ label: 'Aware', path: '/aware-page?view=a' });
        window.__antreaPluginHost!.registerRoute({ path: '/plain-page', tag: 'plain' });
        window.__antreaPluginHost!.registerSidebarEntry({ label: 'Plain', path: '/plain-page', requires: { namespaceScope: true } });

        const entries = getPluginSidebarEntries();
        expect(entries.find(e => e.path === '/aware-page?view=a')?.namespaceAware).toBe(true);
        expect(entries.find(e => e.path === '/plain-page')?.namespaceAware).toBeUndefined();
    });

    test('a malformed `requires` is logged and denies', () => {
        const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
        window.__antreaPluginHost!.registerSidebarEntry({ label: 'Typo', path: '/gated-typo', requires: { clusterAdmin: 'yes' } as never });

        const entry = getPluginSidebarEntries().find(e => e.path === '/gated-typo');
        expect(errorSpy).toHaveBeenCalledWith(expect.stringContaining('malformed "requires"'));
        expect(allowsPluginItem({ summary: admin(true), loaded: true }, entry?.access)).toBe(false);
    });

    test('allowsPluginItem hides a gated item until loaded, then follows the summary', () => {
        const access = [{ clusterAdmin: true }];
        expect(allowsPluginItem({ summary: null, loaded: false }, undefined)).toBe(true);
        expect(allowsPluginItem({ summary: admin(true), loaded: false }, access)).toBe(false);
        expect(allowsPluginItem({ summary: admin(false), loaded: true }, access)).toBe(false);
        expect(allowsPluginItem({ summary: admin(true), loaded: true }, access)).toBe(true);
        expect(allowsPluginItem({ summary: null, loaded: true }, access)).toBe(true);
    });

    test('the host exposes what it publishes to plugins as window.__antreaPluginHost.access', async () => {
        const access = window.__antreaPluginHost!.access!;
        publishPluginAccess(true, admin(true));

        await expect(access.ready()).resolves.toEqual(admin(true));
        expect(access.isClusterAdmin()).toBe(true);
        expect(access.meets({ clusterAdmin: true })).toBe(true);

        publishPluginAccess(false, null);
    });
});
