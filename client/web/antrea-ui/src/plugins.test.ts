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

import { loadPlugins, validateNavItem, type PluginManifest } from './plugins';

function jsonResponse(body: unknown, status = 200): Response {
    return new Response(JSON.stringify(body), { status });
}

function manifest(overrides: Partial<PluginManifest> = {}): PluginManifest {
    return { name: 'pod-counter', version: '0.1.0', entry: 'index.js', tag: 'antrea-plugin-pod-counter', ...overrides };
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

describe('validateNavItem', () => {
    test('a plugin with no navItem passes through unchanged', () => {
        const m = manifest();
        expect(validateNavItem(m, new Set())).toEqual(m);
    });

    test('a navItem.path colliding with a built-in route is dropped', () => {
        const m = manifest({ navItem: { label: 'Settings', path: '/settings' } });
        const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});

        const result = validateNavItem(m, new Set());

        expect(result.navItem).toBeUndefined();
        expect(errorSpy).toHaveBeenCalledWith(expect.stringContaining('collides with a built-in route'));
    });

    test('a navItem.path already claimed by another plugin is dropped', () => {
        const m = manifest({ navItem: { label: 'Pod Counter', path: '/plugin/pod-counter' } });
        const seenPaths = new Set(['plugin/pod-counter']);
        const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});

        const result = validateNavItem(m, seenPaths);

        expect(result.navItem).toBeUndefined();
        expect(errorSpy).toHaveBeenCalledWith(expect.stringContaining('already claimed by another plugin'));
    });

    test('a unique, non-reserved navItem.path is kept and recorded in seenPaths', () => {
        const m = manifest({ navItem: { label: 'Pod Counter', path: '/plugin/pod-counter' } });
        const seenPaths = new Set<string>();

        const result = validateNavItem(m, seenPaths);

        expect(result.navItem).toEqual(m.navItem);
        expect(seenPaths.has('plugin/pod-counter')).toBe(true);
    });
});
