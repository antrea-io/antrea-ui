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

// Registration API for Antrea UI plugins — both whole-page plugins (registerRoute,
// registerSidebarEntry) and plugins that extend an existing page (registerEdgeExtraRenderer,
// registerFlowTableColumnsProcessor). Modeled on Headlamp's plugin registry. Call one of the
// register*() functions below as an import side effect in your plugin's entry module, the same
// module that calls customElements.define(...) for any custom element the registration
// references (e.g. a route's `tag`).
//
// Every function here is a thin wrapper over window.__antreaPluginHost, which the host app sets
// up before it import()s any plugin. Going through `window` (rather than shared module state)
// matters because your plugin is built as its own standalone bundle with its own copy of any
// npm dependency — a registry living in a normally-imported package would not be the same
// object your plugin and the host both see.
//
// EdgeSelection/FlowEntry-derived types are re-exported from @antrea/ui-components (a peer
// dependency, types-only — nothing from it ends up in your plugin's bundle) so you don't
// hand-roll the shape the host expects.

export type { EdgeSelection, EdgeExtraRenderer, FlowEntry, FlowTableColumn, FlowTableColumnsProcessor } from '@antrea/ui-components';
import type { EdgeExtraRenderer, FlowTableColumnsProcessor } from '@antrea/ui-components';

/** A route a whole-page plugin wants the host to mount. `tag` is the custom element the
 * plugin's entry module registers via customElements.define(...); the host renders it with
 * the same `token`/session-refresh wiring as its own built-in pages. */
export interface PluginRoute {
    // The in-app route path, e.g. "/plugin/pod-counter". Must not start with "/plugins/" —
    // that prefix is reserved for serving plugin static assets, so a route there would never
    // reach the SPA on a hard refresh or direct link. Must not collide with a built-in route
    // or another plugin's — the host drops (and logs) whichever registration loses the race.
    path: string;
    tag: string;
}

/** A sidebar entry a whole-page plugin wants the host to render. Independent of registerRoute
 * so a plugin can add a sidebar entry without a route (e.g. an external link) or vice versa —
 * pass the same `path` as the matching PluginRoute to link the two. */
export interface PluginSidebarEntry {
    label: string;
    path: string;
    // SVG path "d" data for a 16x16 (viewBox "0 0 16 16") icon, matching the style of the
    // built-in nav icons. Optional — entries without one just show a label.
    icon?: string;
}

// Kept in sync by hand with the identically-named interface in antrea-ui/src/plugins.ts, which
// is what actually populates window.__antreaPluginHost — this side only needs to describe its
// shape well enough to type-check the wrapper functions below.
export interface AntreaPluginHost {
    registerRoute(route: PluginRoute): void;
    registerSidebarEntry(entry: PluginSidebarEntry): void;
    registerEdgeExtraRenderer(fn: EdgeExtraRenderer): void;
    registerFlowTableColumnsProcessor(fn: FlowTableColumnsProcessor): void;
}

declare global {
    interface Window { __antreaPluginHost?: AntreaPluginHost; }
}

function host(): AntreaPluginHost {
    const h = window.__antreaPluginHost;
    if (!h) {
        throw new Error(
            '@antrea/ui-plugin-sdk: registerX() called before Antrea UI initialized its plugin ' +
            'host. Call it from your plugin entry module\'s top level or during customElements ' +
            'define/connectedCallback — not before Antrea UI itself has loaded.'
        );
    }
    return h;
}

/** Adds a whole new page to Antrea UI, rendering `route.tag`'s custom element at `route.path`. */
export function registerRoute(route: PluginRoute): void {
    host().registerRoute(route);
}

/** Adds a sidebar entry linking to `entry.path`. */
export function registerSidebarEntry(entry: PluginSidebarEntry): void {
    host().registerSidebarEntry(entry);
}

/** Renders extra content into the service map's edge details card for the selected edge.
 * Return `null` to render nothing for a given selection. */
export function registerEdgeExtraRenderer(fn: EdgeExtraRenderer): void {
    host().registerEdgeExtraRenderer(fn);
}

/** Inserts, removes, updates, or reorders columns in the flow list table. Receives the current
 * column list (built-ins plus any earlier plugin's additions) and returns the new list. */
export function registerFlowTableColumnsProcessor(fn: FlowTableColumnsProcessor): void {
    host().registerFlowTableColumnsProcessor(fn);
}
