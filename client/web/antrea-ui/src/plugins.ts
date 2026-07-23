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

// Runtime plugin loading: plugins are dropped into /etc/plugins in the running pod (not baked
// into this image, see build/frontend.dockerfile and build/scripts/plugin-index-builder.sh),
// nginx serves them under /plugins/, and this module discovers and loads them at app startup.
//
// A plugin is a directory containing:
//   - manifest.json: declares name/entry/tag and, optionally, a navItem to add a sidebar entry
//     and route for it.
//   - <entry>: an ES module that registers a custom element (customElements.define(tag, ...))
//     as an import side effect, the same pattern @antrea/ui-components itself uses.
//
// Adding a new extension point later (e.g. a host API object passed to a register() callback)
// can be layered onto this manifest without breaking existing plugins.

export interface PluginNavItem {
    label: string;
    // The in-app route path, e.g. "/plugin/pod-counter". Must not start with "/plugins/" —
    // that prefix is reserved by nginx for serving plugin static assets (see
    // build/charts/antrea-ui/templates/_nginx_conf.tpl), so a route there would never reach
    // the SPA on a hard refresh or direct link.
    path: string;
    // SVG path "d" data for a 16x16 (viewBox "0 0 16 16") icon, matching the style of the
    // built-in nav icons in nav.tsx. Optional — items without one just show a label.
    icon?: string;
}

export interface PluginManifest {
    name: string;
    version: string;
    entry: string;
    tag: string;
    navItem?: PluginNavItem;
}

// Top-level routes owned by Antrea UI itself. A plugin's navItem.path must not collide with
// these — react-router's behavior with two children registered under the same path is
// undefined, and a colliding plugin could silently shadow a built-in page.
const RESERVED_PATHS = new Set(['', 'summary', 'traceflow', 'flows', 'settings']);

function stripLeadingSlash(path: string): string {
    return path.replace(/^\//, '');
}

// Drops navItem from a manifest if its path collides with a built-in route or with a
// navItem.path already claimed by an earlier plugin, logging why. `seenPaths` accumulates
// claimed paths across the whole loadPlugins() call so later plugins are checked against
// earlier ones too.
export function validateNavItem(manifest: PluginManifest, seenPaths: Set<string>): PluginManifest {
    if (!manifest.navItem) return manifest;

    const normalizedPath = stripLeadingSlash(manifest.navItem.path);
    if (RESERVED_PATHS.has(normalizedPath)) {
        console.error(
            `plugin "${manifest.name}": navItem.path "${manifest.navItem.path}" collides with ` +
            `a built-in route, dropping its nav entry`
        );
        return { ...manifest, navItem: undefined };
    }
    if (seenPaths.has(normalizedPath)) {
        console.error(
            `plugin "${manifest.name}": navItem.path "${manifest.navItem.path}" is already ` +
            `claimed by another plugin, dropping its nav entry`
        );
        return { ...manifest, navItem: undefined };
    }

    seenPaths.add(normalizedPath);
    return manifest;
}

export async function loadPlugins(): Promise<PluginManifest[]> {
    let manifests: PluginManifest[];
    try {
        const res = await fetch('/plugins/index.json');
        if (!res.ok) return [];
        manifests = await res.json();
    } catch (e) {
        console.error('failed to fetch plugin index', e);
        return [];
    }

    const seenPaths = new Set<string>();
    const loaded: PluginManifest[] = [];
    for (const manifest of manifests) {
        try {
            await import(/* @vite-ignore */ `/plugins/${manifest.name}/${manifest.entry}`);
            loaded.push(validateNavItem(manifest, seenPaths));
        } catch (e) {
            console.error(`failed to load plugin "${manifest.name}"`, e);
        }
    }
    return loaded;
}
