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

import { loadPlugins, isCompatible, isValidRange, getIncompatiblePlugins, incompatiblePluginMessage, dedupeByPath, getPluginRoutes, resolveParentPaths, getLandingPageTabs, type PluginManifest, type PluginRoute, type PluginSidebarEntry, type LandingPageTab } from './plugins';

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

        // Only the index was fetched, and no "failed to load plugin" error: import() was never tried.
        expect(fetch).toHaveBeenCalledTimes(1);
        expect(errorSpy).toHaveBeenCalledTimes(1);
        const [info] = getIncompatiblePlugins();
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
    // The same cases the backend's TestValidateCompatibleAntreaUI runs against its own library.
    test.each(rangeCases)('$range is valid: $valid', ({ range, valid }) => {
        expect(isValidRange(range)).toBe(valid);
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

    test('a path colliding with a built-in route is dropped', () => {
        const items = [{ path: '/settings' }];
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

    test('a parentPath matching a built-in page is kept and normalized', () => {
        const result = resolveParentPaths([entry({ parentPath: '/flows' })]);
        expect(result).toEqual([entry({ parentPath: 'flows' })]);
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

    test('a tab id colliding with the built-in Network Traffic & Inventory tab is dropped', () => {
        const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
        window.__antreaPluginHost!.registerLandingPageTab({ id: 'traffic-inventory', label: 'Fake Overview', tag: 'antrea-fake-overview' });

        expect(getLandingPageTabs().find(t => t.id === 'traffic-inventory')).toBeUndefined();
        expect(errorSpy).toHaveBeenCalledWith(expect.stringContaining('collides with the built-in Network Traffic & Inventory tab'));
    });

    test('a tab id already claimed by an earlier plugin is dropped', () => {
        const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
        window.__antreaPluginHost!.registerLandingPageTab({ id: 'dup-tab', label: 'First', tag: 'antrea-first' });
        window.__antreaPluginHost!.registerLandingPageTab({ id: 'dup-tab', label: 'Second', tag: 'antrea-second' });

        expect(getLandingPageTabs().filter(t => t.id === 'dup-tab')).toEqual([{ id: 'dup-tab', label: 'First', tag: 'antrea-first' }]);
        expect(errorSpy).toHaveBeenCalledWith(expect.stringContaining('already claimed by another plugin'));
    });
});
