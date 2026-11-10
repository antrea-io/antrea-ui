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
// EdgeSelection/FlowEntry-derived and access types are re-exported from @antrea/ui-components
// (a peer dependency, types-only — nothing from it ends up in your plugin's bundle) so you don't
// hand-roll the shape the host expects.

export type { EdgeSelection, EdgeExtraRenderer, FlowEntry, FlowTableColumn, FlowTableColumnsProcessor } from '@antrea/ui-components';
export type {
    AccessSummary, NamespaceAccessSummary, ResourceQuery, Verdict, PluginAccessRequirement,
    PluginResourceRequirement, PluginHostAccess,
} from '@antrea/ui-components';
import type { EdgeExtraRenderer, FlowTableColumnsProcessor, PluginAccessRequirement, PluginHostAccess } from '@antrea/ui-components';

// `requires` (below, on PluginRoute, PluginSidebarEntry and LandingPageTab) hides an item from
// users who don't meet it, evaluated by the host against the user's access summary:
//   - `{ clusterAdmin: true }`: cluster admins only. It decides alone: `namespaceScope` and
//     `resources` beside it are ignored. `false` means "not written".
//   - `{ resources: [{ group, resource, verb }] }`: users whose RBAC allows every query listed;
//     `{ resources: { allOf: [...] } }` says the same, and `{ resources: { anyOf: [...] } }`
//     needs only one.
//   - `{ namespaceScope: true }`: the item is a Namespace-aware page (one that shows content for
//     one Namespace or all, keeping the choice in `?namespace=<name>` or `?namespace=_all`). Its
//     `resources` list the permissions it reads (a plain array or `anyOf`; any one allowed is
//     enough, and `allOf` is rejected), and it shows to a user for whom those are not denied, who
//     may list Namespaces, or who is named in at least one RoleBinding. With no `resources` it
//     shows to every signed-in user. So it widens access: a user with a RoleBinding but without
//     the permission sees the item and gets the API's 403, which the page must handle.
// Gated items stay hidden until the summary has loaded, a malformed `requires` hides the item, and
// a summary that failed to load shows it. This is a UI hint, not authorization: Kubernetes RBAC
// still authorizes every API call your plugin makes. A host that predates `namespaceScope` or the
// object form of `resources` hides such an item from everyone, and the backend rejects a manifest
// that uses them, so raise your manifest's `compatibleAntreaUI` to the release that adds them.

/** A route a whole-page plugin wants the host to mount. `tag` is the custom element the
 * plugin's entry module registers via customElements.define(...); the host renders it with the
 * same session-expiry wiring as its own built-in pages. Your element receives no credential:
 * calls to the Antrea UI backend authenticate with the session cookie the browser already holds,
 * so a plain fetch with credentials: 'include' is enough, and you should add nothing of your own.
 * If you want apiFetch()/apiFetchJSON(), they live in @antrea/ui-components/api (not the root
 * entry, which registers every custom element and would throw in the host); that package is not
 * published yet, so it has to be added to your plugin as a file: dependency. The SDK itself
 * stays types-only and does not re-export them. */
export interface PluginRoute {
    // The in-app route path, e.g. "/plugin/pod-counter". Must not start with "/api/" — that
    // prefix is reserved for the backend API (including serving plugin static assets), so a
    // route there would never reach the SPA on a hard refresh or direct link. Must not collide
    // with a built-in route or another plugin's — the host drops (and logs) whichever
    // registration loses the race.
    path: string;
    tag: string;
    // A user who doesn't meet it gets the host's "You do not have permission" page here instead,
    // and the sidebar entry at the same `path` is hidden too.
    requires?: PluginAccessRequirement;
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
    // Nests this entry under another top-level sidebar entry, rendered as a collapsible group
    // (see @antrea/ui-components' antrea-nav-group) instead of a flat item. Either a built-in
    // page's path ("overview", "flows", "summary", "traceflow", "supportbundle", "settings") or
    // another plugin's own top-level entry path (with or without a leading slash — both are
    // accepted). Nesting is one level deep: pointing this at an entry that itself has a
    // parentPath is invalid and the host drops (and logs) the nesting, falling back to a
    // top-level entry.
    //
    // "overview", "flows", "summary", "traceflow" and "supportbundle" are also gated by per-user
    // RBAC and don't render at all for a user who fails that gate — nesting under any of them
    // falls back to top-level the same way for such a user, but silently and per-user, since
    // the parentPath itself is always valid.
    parentPath?: string;
    // Hides this entry, and any entries nested under it, from users who don't meet it. An entry
    // also inherits the `requires` of the route registered at its `path`.
    requires?: PluginAccessRequirement;
}

/** A tab a plugin wants added to the Overview landing page, alongside the built-in "Inventory &
 * Events" tab — e.g. the ANS plugin's "Security" tab. `tag` is the custom element
 * the plugin's entry module registers via customElements.define(...); the host mounts it the
 * same way it mounts the built-in tab, with the same session-expiry wiring. `id` must not be
 * "traffic-inventory" (reserved for the built-in tab) or collide with another plugin's tab id —
 * the host drops (and logs) whichever registration loses the race. */
export interface LandingPageTab {
    id: string;
    label: string;
    tag: string;
    // Leaves the tab out for users who don't meet it; a ?tab= naming it falls back to the
    // built-in tab.
    requires?: PluginAccessRequirement;
}

// Kept in sync by hand with the identically-named interface in antrea-ui/src/plugins.ts, which
// is what actually populates window.__antreaPluginHost — this side only needs to describe its
// shape well enough to type-check the wrapper functions below.
export interface AntreaPluginHost {
    registerRoute(route: PluginRoute): void;
    registerSidebarEntry(entry: PluginSidebarEntry): void;
    registerEdgeExtraRenderer(fn: EdgeExtraRenderer): void;
    registerFlowTableColumnsProcessor(fn: FlowTableColumnsProcessor): void;
    registerLandingPageTab(tab: LandingPageTab): void;
    // Optional so a plugin can still tell a host predating it apart (see access() below).
    access?: PluginHostAccess;
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

/** Adds a tab to the Overview landing page, rendering `tab.tag`'s custom element when selected. */
export function registerLandingPageTab(tab: LandingPageTab): void {
    host().registerLandingPageTab(tab);
}

/** The user's permissions, for hiding parts of your own UI: `can()`, `isClusterAdmin()`,
 * `accessibleNamespaces()`, `forNamespace()`, and `subscribe()` for changes. Answers are only
 * meaningful once `ready()` has resolved (before that the host has no summary and every check
 * fails open), so await it before deciding what to render. Throws on a host too old to provide
 * it. */
export function access(): PluginHostAccess {
    const a = host().access;
    if (!a) {
        throw new Error('@antrea/ui-plugin-sdk: this Antrea UI host does not provide access(); upgrade Antrea UI.');
    }
    return a;
}
