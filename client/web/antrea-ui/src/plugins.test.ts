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

import { loadPlugins, dedupeByPath, getPluginRoutes, type PluginManifest, type PluginRoute } from './plugins';

function jsonResponse(body: unknown, status = 200): Response {
    return new Response(JSON.stringify(body), { status });
}

function manifest(overrides: Partial<PluginManifest> = {}): PluginManifest {
    return { name: 'pod-counter', version: '0.1.0', entry: 'index.js', ...overrides };
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

describe('getPluginRoutes / window.__antreaPluginHost', () => {
    test('registerRoute makes the route show up in getPluginRoutes', () => {
        window.__antreaPluginHost!.registerRoute({ path: '/plugin/test-registration', tag: 'antrea-plugin-test-registration' } satisfies PluginRoute);

        expect(getPluginRoutes()).toContainEqual({ path: '/plugin/test-registration', tag: 'antrea-plugin-test-registration' });
    });
});
