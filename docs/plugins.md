# Frontend Plugins

Antrea UI supports loading frontend plugins at runtime, without rebuilding
the `antrea-ui-frontend` image. A plugin adds its own page and sidebar entry
to the UI. The mechanism is modeled on
[Headlamp's plugin system](https://headlamp.dev/docs/latest/development/plugins/getting-started):
a plugin is a self-contained JS bundle plus a manifest, discovered and
loaded by the host app at runtime instead of being compiled in. See
[`plugins/examples/pod-counter`](../plugins/examples/pod-counter) for a
complete, minimal example.

## How it works

1. A plugin is delivered as a Kubernetes `ConfigMap`, in antrea-ui's own
   namespace, labeled to match `plugins.labelSelector` (default:
   `plugins.antrea-ui.io/plugin=true`). Its data holds `manifest.json` plus
   the plugin's JS entry file.
2. The Go backend watches ConfigMaps matching that label (see
   [`pkg/plugins`](../pkg/plugins) and
   [`pkg/server/api/plugins.go`](../pkg/server/api/plugins.go)) and serves
   them, unauthenticated, at `GET /api/v1/plugins/index.json` (the merged
   list of manifests) and `GET /api/v1/plugins/<name>/<file>` (any file from
   that plugin's ConfigMap). A ConfigMap can be created, updated, or deleted
   at any time — the backend picks up the change immediately, with no
   antrea-ui restart.
3. On load, Antrea UI fetches `/api/v1/plugins/index.json` and `import()`s
   each plugin's JS module at runtime — the code doesn't need to exist when
   the frontend is built. See [`plugins.ts`](../client/web/antrea-ui/src/plugins.ts).
4. Each module registers a [Lit](https://lit.dev) custom element via
   `customElements.define(...)`, same as `@antrea/ui-components` does for
   Antrea UI's own pages, and tells the host about it by calling one of
   `@antrea/ui-plugin-sdk`'s `registerX()` functions — `registerRoute` and
   `registerSidebarEntry` for a whole new page, or one of the "Extending an
   existing page" functions below to extend one Antrea UI already ships.
   This is modeled on
   [Headlamp's plugin registry](https://headlamp.dev/docs/latest/development/plugins/functionality/).

## The manifest

```json
{
  "name": "pod-counter",
  "version": "0.1.0",
  "entry": "index.js"
}
```

| Field | Required | Description |
| --- | --- | --- |
| `name` | yes | Unique name; also the path segment used to serve the plugin, e.g. `/api/v1/plugins/<name>/`. |
| `version` | yes | Informational only. |
| `entry` | yes | Plugin's JS module filename; must be a key in the same ConfigMap's data. |

That's the whole schema — the manifest only carries enough for the host to
fetch and `import()` the right file. Everything that affects the UI (routes,
sidebar entries, page extensions) is registered in code, via
`@antrea/ui-plugin-sdk`, so a plugin's actual shape is never split between
JSON and JS.

## Writing a plugin

A plugin is a standalone package — not part of the `client/web` Yarn
workspace, and it doesn't depend on `@antrea/ui-components` internals. It
relies on: `@antrea/ui-plugin-sdk` to register itself with the host, the
`token` property/attribute the host sets on its custom element (for
authenticated calls), and Antrea UI's REST API. Its own `vite.config.ts`
must bundle dependencies like `lit` in, rather than externalizing them
(unlike `@antrea/ui-components`) — there's no host-provided import map for
a runtime `import()`.

`plugins/examples/pod-counter/src/index.ts`:

```ts
import { LitElement, html } from 'lit';
import { property, state } from 'lit/decorators.js';
import { registerRoute, registerSidebarEntry } from '@antrea/ui-plugin-sdk';

class AntreaPluginPodCounter extends LitElement {
    @property() token = '';
    @state() private _count: number | null = null;

    connectedCallback() {
        super.connectedCallback();
        fetch('/api/v1/k8s/api/v1/pods', {
            headers: { Authorization: `Bearer ${this.token}` },
        })
            .then((res) => res.json())
            .then((data) => { this._count = data.items?.length ?? 0; });
    }

    render() {
        return html`<h1>Pods in cluster: ${this._count ?? '...'}</h1>`;
    }
}

customElements.define('antrea-plugin-pod-counter', AntreaPluginPodCounter);

registerRoute({ path: '/plugin/pod-counter', tag: 'antrea-plugin-pod-counter' });
registerSidebarEntry({ label: 'Pod Counter', path: '/plugin/pod-counter' });
```

`registerRoute`'s `path` must not collide with a built-in route or another
plugin's; the host drops (and logs) whichever registration loses the race. `registerSidebarEntry` is independent of
`registerRoute`, so a plugin can add a sidebar entry without a route (e.g.
an external link) or vice versa; pass the same `path` to both to link them,
as above. `registerSidebarEntry`'s optional `icon` is SVG path `d` data,
16x16 (`viewBox="0 0 16 16"`), matching the built-in nav icons' style.

`@antrea/ui-plugin-sdk` is a devDependency resolved from this repo's
workspace (`file:../../../client/web/antrea-ui-plugin-sdk` in
`package.json`) — build it once before building any example plugin:

```bash
cd client/web/antrea-ui-plugin-sdk && yarn build
cd ../../../plugins/examples/pod-counter
npm install && npm run build   # vite build, then copies manifest.json into dist/
```

If your plugin needs a K8s API path that isn't already proxied, add it to
`allowedK8sPaths` in [`pkg/server/api/k8s.go`](../pkg/server/api/k8s.go) —
this list is coarse and not scoped to your plugin (every path added becomes
reachable by any authenticated Antrea UI user), so only add what's needed.

RBAC for that path is **not** added to Antrea UI's own `clusterroles.yaml`.
Instead, ship your own `ClusterRole` labeled
`rbac.antrea-ui.io/aggregate-to-antrea-ui-admin: "true"`, which
[aggregates](https://kubernetes.io/docs/reference/access-authn-authz/rbac/#aggregated-clusterroles)
into the `antrea-ui-admin` ClusterRole automatically — the one Antrea UI
impersonates for K8s API calls made on behalf of the UI user, which is what
the K8s API proxy your plugin calls through does — see
[`plugins/examples/pod-counter/clusterrole.yaml`](../plugins/examples/pod-counter/clusterrole.yaml).
Creating (and RBAC-scoping) that ClusterRole is the responsibility of
whoever deploys the plugin, not Antrea UI itself.

## Extending an existing page

Everything above adds a whole new page. A plugin can instead extend a page
Antrea UI already ships — e.g. render extra content in the service map's
edge details card, or add a column to the flow list table — via
`@antrea/ui-plugin-sdk`, modeled on
[Headlamp's plugin registry](https://headlamp.dev/docs/latest/development/plugins/functionality/)
(`registerDetailsViewSection`, `registerResourceTableColumnsProcessor`).

Call one of the SDK's `registerX()` functions as an import side effect in
your plugin's entry module — the same place a whole-page plugin calls
`customElements.define(...)`. A plugin can do both in the same module.

```ts
import { registerEdgeExtraRenderer, registerFlowTableColumnsProcessor } from '@antrea/ui-plugin-sdk';

// Renders into the service map's edge details card for the currently
// selected edge. Return null to render nothing for a given selection.
registerEdgeExtraRenderer((selection) => {
    const el = document.createElement('a');
    el.href = `/plugin/my-plugin?source=${selection.source}&target=${selection.target}`;
    el.textContent = 'Open in My Plugin';
    return el;
});

// Inserts, removes, updates, or reorders flow list table columns. Receives
// the current list — built-ins plus any earlier plugin's additions — and
// returns the new list.
registerFlowTableColumnsProcessor((columns) => [
    ...columns,
    { key: 'my-column', label: 'My Column', render: (entry) => entry.flow.k8s.flowType },
]);
```

All registration functions, including the whole-new-page ones from "Writing
a plugin" above:

| Function | Extends | Notes |
| --- | --- | --- |
| `registerRoute` | Router | Adds a whole new page at `route.path`, rendering `route.tag`'s custom element. |
| `registerSidebarEntry` | Sidebar | Adds a nav entry linking to `entry.path`. |
| `registerEdgeExtraRenderer` | Service map edge details card | Called with an `EdgeSelection` on each selection change; return `null` to render nothing. |
| `registerFlowTableColumnsProcessor` | Flow list table | Plugin-added columns aren't sortable — only built-in columns carry the sort key. |

These functions call into a small registry the host sets up on `window`
before loading any plugin — see
[`plugins.ts`](../client/web/antrea-ui/src/plugins.ts) for the host side and
[`antrea-flow-visibility-page.ts`](../client/web/antrea-ui-components/src/pages/antrea-flow-visibility-page.ts)
for how the registered functions are consumed. `@antrea/ui-plugin-sdk` only
re-exports types from `@antrea/ui-components` (a peer dependency,
types-only — nothing from it ends up in your plugin's bundle) so the shapes
stay in sync with what the host actually expects.

## Trying it locally

Antrea UI only makes sense running against a real cluster, so test against
one directly rather than a standalone dev server. The commands below assume
`@antrea/ui-plugin-sdk` is already built (see "Writing a plugin" above), and
that `<namespace>` is the namespace Antrea UI itself is installed in — the
backend only watches ConfigMaps in its own namespace.

```bash
cd plugins/examples/pod-counter
npm install && npm run build

kubectl create configmap pod-counter-plugin -n <namespace> \
  --from-file=dist/index.js --from-file=dist/manifest.json
kubectl label configmap pod-counter-plugin -n <namespace> \
  plugins.antrea-ui.io/plugin=true
kubectl apply -f clusterrole.yaml
```

The last command grants the plugin's own RBAC (see above); without it, the
plugin's page loads but its K8s API call gets a 403. No Helm upgrade, no pod
restart otherwise: the backend's ConfigMap watch picks this up
immediately (note ConfigMaps are capped at 1MiB total, so this only works
for reasonably small plugins). Refresh the browser and the plugin's page
shows up. To iterate on the plugin's code:

```bash
npm run build
kubectl delete configmap pod-counter-plugin -n <namespace>
kubectl create configmap pod-counter-plugin -n <namespace> \
  --from-file=dist/index.js --from-file=dist/manifest.json
kubectl label configmap pod-counter-plugin -n <namespace> \
  plugins.antrea-ui.io/plugin=true
```

To remove the plugin: `kubectl delete configmap pod-counter-plugin -n
<namespace>` — it disappears from `/api/v1/plugins/index.json` immediately.

This same flow — build the plugin, create the labeled ConfigMap, install
Antrea UI — is what
[`test/e2e/plugin_test.go`](../test/e2e/plugin_test.go) automates as a real
e2e test in [`kind_e2e.yml`](../.github/workflows/kind_e2e.yml), against an
actual Kind cluster rather than a standalone container.

## Future work

* **More extension points.** `@antrea/ui-plugin-sdk` (see "Extending an
  existing page" above) currently only covers the service map's edge
  details card and the flow list table. Other built-in pages (Summary,
  Traceflow) don't expose any extension points yet — adding one means the
  same shape: a new registration function in the SDK, a registry entry in
  `plugins.ts`, and the target Lit component consuming the registered
  functions when it renders.
