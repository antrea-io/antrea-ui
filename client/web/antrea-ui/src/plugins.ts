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
//   - manifest.json: bare metadata (name/version/entry) — just enough to know which JS module
//     to fetch and import(). It carries no UI-affecting fields; those are all registered in
//     code (see below), so a plugin's actual shape (routes, sidebar entries, page extensions)
//     is never split between JSON and JS.
//   - <entry>: an ES module that, as an import side effect, registers a custom element
//     (customElements.define(tag, ...)) for anything it wants to render, and calls into the
//     plugin registry below via @antrea/ui-plugin-sdk to tell the host about it.
//
// The registry (modeled on Headlamp's plugin API) is a plain object exposed on `window` —
// plugin bundles are separate Vite builds with their own copy of any npm dependency, so a
// module-scoped registry inside a shared package would not be the same object identity as the
// one this app's code sees; `window` is the only thing plugin code and host code both reach.
// It must exist before any plugin's `import()` below runs, since a plugin registers by calling
// an SDK function during its own module evaluation.

import type { EdgeExtraRenderer, FlowTableColumnsProcessor } from '@antrea/ui-components';

export interface PluginRoute {
    path: string;
    tag: string;
}

export interface PluginSidebarEntry {
    label: string;
    path: string;
    icon?: string;
}

export interface AntreaPluginHost {
    registerRoute(route: PluginRoute): void;
    registerSidebarEntry(entry: PluginSidebarEntry): void;
    registerEdgeExtraRenderer(fn: EdgeExtraRenderer): void;
    registerFlowTableColumnsProcessor(fn: FlowTableColumnsProcessor): void;
}

const pluginRoutes: PluginRoute[] = [];
const pluginSidebarEntries: PluginSidebarEntry[] = [];
const edgeExtraRenderers: EdgeExtraRenderer[] = [];
const flowTableColumnsProcessors: FlowTableColumnsProcessor[] = [];

declare global {
    interface Window { __antreaPluginHost?: AntreaPluginHost; }
}

window.__antreaPluginHost = {
    registerRoute: route => pluginRoutes.push(route),
    registerSidebarEntry: entry => pluginSidebarEntries.push(entry),
    registerEdgeExtraRenderer: fn => edgeExtraRenderers.push(fn),
    registerFlowTableColumnsProcessor: fn => flowTableColumnsProcessors.push(fn),
};

// Top-level routes owned by Antrea UI itself. A plugin's route/sidebar entry path must not
// collide with these — react-router's behavior with two children registered under the same
// path is undefined, and a colliding plugin could silently shadow a built-in page.
const RESERVED_PATHS = new Set(['', 'summary', 'traceflow', 'flows', 'settings']);

function stripLeadingSlash(path: string): string {
    return path.replace(/^\//, '');
}

// Drops any route/sidebar entry whose path collides with a built-in route or with a path
// already claimed by an earlier plugin, logging why. Applied once after every plugin has
// finished registering (see loadPlugins()) — unlike the old manifest-driven navItem, there's
// no way to validate a path before running the plugin code that registers it.
export function dedupeByPath<T extends { path: string }>(items: T[], kind: string): T[] {
    const seenPaths = new Set<string>();
    const kept: T[] = [];
    for (const item of items) {
        const normalizedPath = stripLeadingSlash(item.path);
        if (RESERVED_PATHS.has(normalizedPath)) {
            console.error(`plugin ${kind} for path "${item.path}" collides with a built-in route, dropping it`);
            continue;
        }
        if (seenPaths.has(normalizedPath)) {
            console.error(`plugin ${kind} for path "${item.path}" is already claimed by another plugin, dropping it`);
            continue;
        }
        seenPaths.add(normalizedPath);
        kept.push(item);
    }
    return kept;
}

export function getPluginRoutes(): PluginRoute[] {
    return dedupeByPath(pluginRoutes, 'route');
}

export function getPluginSidebarEntries(): PluginSidebarEntry[] {
    return dedupeByPath(pluginSidebarEntries, 'sidebar entry');
}

export function getEdgeExtraRenderers(): EdgeExtraRenderer[] {
    return edgeExtraRenderers;
}

export function getFlowTableColumnsProcessors(): FlowTableColumnsProcessor[] {
    return flowTableColumnsProcessors;
}

export interface PluginManifest {
    name: string;
    version: string;
    entry: string;
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

    const loaded: PluginManifest[] = [];
    for (const manifest of manifests) {
        try {
            await import(/* @vite-ignore */ `/plugins/${manifest.name}/${manifest.entry}`);
            loaded.push(manifest);
        } catch (e) {
            console.error(`failed to load plugin "${manifest.name}"`, e);
        }
    }
    return loaded;
}
