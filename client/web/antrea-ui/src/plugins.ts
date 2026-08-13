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

// Runtime plugin loading: plugins are delivered as labeled ConfigMaps in antrea-ui's own
// namespace (see pkg/plugins and pkg/server/api/plugins.go), watched by the Go backend, which
// serves them under /api/v1/plugins/. A ConfigMap can be created or deleted at any time - the
// index reflects the change on the next fetch, with no antrea-ui restart required. This module
// fetches that index and loads the plugins it lists at app startup.
//
// A plugin's ConfigMap data contains:
//   - manifest.json: bare metadata (name/version/entry), plus two fields this host doesn't act
//     on (route/federation - see apis/v1/plugins.go) for a plugin whose whole page is a module
//     federation remote, letting an Angular-based host lazily load it. entry is unaffected
//     either way: it's always a plain ES module this host can import() unconditionally, exactly
//     as below, regardless of whether a manifest also carries route/federation.
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
// The host and the SDK must agree on AntreaPluginHost's shape (it's the contract every plugin's
// registerX() call goes through), but only as types — importing them with `import type` here
// costs nothing at runtime, so there's no reason to hand-duplicate the interface as the actual
// host-side implementation used to.
import type { AntreaPluginHost, PluginRoute, PluginSidebarEntry } from '@antrea/ui-plugin-sdk';

export type { PluginRoute, PluginSidebarEntry };

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

// A plugin's route/sidebar entry path must also not fall under this prefix: nginx proxies it
// straight to the backend (see /api/v1/plugins/ above), so a hard refresh or direct link to
// such a path would 404 against the backend instead of reaching the SPA.
const RESERVED_PREFIX = 'api/';

function stripLeadingSlash(path: string): string {
    return path.replace(/^\//, '');
}

// Drops any route/sidebar entry whose path collides with a built-in route, falls under a
// backend-served prefix, or is already claimed by an earlier plugin, logging why. Applied once
// after every plugin has finished registering (see loadPlugins()) — unlike the old
// manifest-driven navItem, there's no way to validate a path before running the plugin code
// that registers it.
export function dedupeByPath<T extends { path: string }>(items: T[], kind: string): T[] {
    const seenPaths = new Set<string>();
    const kept: T[] = [];
    for (const item of items) {
        const normalizedPath = stripLeadingSlash(item.path);
        if (RESERVED_PATHS.has(normalizedPath) || normalizedPath.startsWith(RESERVED_PREFIX)) {
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

// route/federation are declared here for parity with apis/v1/plugins.go, but this host doesn't
// act on them - it has no module federation loader, and has no reason to grow one, since it
// keeps import()-ing entry eagerly for every plugin regardless (see loadPlugins() below).
export interface PluginManifest {
    name: string;
    version: string;
    entry: string;
    route?: { path: string; sidebarLabel: string; icon?: string };
    federation?: { remoteEntry: string; exposedModule: string };
}

export async function loadPlugins(): Promise<PluginManifest[]> {
    let manifests: PluginManifest[];
    try {
        const res = await fetch('/api/v1/plugins/index.json');
        if (!res.ok) return [];
        manifests = await res.json();
    } catch (e) {
        console.error('failed to fetch plugin index', e);
        return [];
    }

    const loaded: PluginManifest[] = [];
    for (const manifest of manifests) {
        try {
            await import(/* @vite-ignore */ `/api/v1/plugins/${encodeURIComponent(manifest.name)}/${encodeURIComponent(manifest.entry)}`);
            loaded.push(manifest);
        } catch (e) {
            console.error(`failed to load plugin "${manifest.name}"`, e);
        }
    }
    return loaded;
}
