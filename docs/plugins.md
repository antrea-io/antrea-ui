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

1. A plugin is delivered as a Kubernetes `ConfigMap`, in the namespace the
   backend watches for plugins (`plugins.namespace`, default: antrea-ui's own
   release namespace), labeled to match `plugins.labelSelector` (default:
   `ui.antrea.io/plugin=true`). Its data holds `manifest.json` plus
   the plugin's JS entry file.

   The RBAC this requires (`get`/`list`/`watch` on ConfigMaps) is granted on
   every ConfigMap in that namespace, not just labeled plugin ones — the
   label selector is applied by the backend's watch, not by RBAC. Since
   antrea-ui is commonly installed into `kube-system`, which can host other,
   unrelated, more sensitive ConfigMaps, set `plugins.namespace` to a
   dedicated namespace if you want plugin ConfigMaps isolated from it. If
   that namespace differs from the release namespace, whoever runs
   `helm install`/`upgrade` needs permission to create a `Role`/`RoleBinding`
   there too — the chart can't grant permissions outside its own release
   namespace.
2. The Go backend watches ConfigMaps matching that label (see
   [`pkg/plugins`](../pkg/plugins) and
   [`pkg/server/api/plugins.go`](../pkg/server/api/plugins.go)) and serves
   them, unauthenticated, at `GET /api/v1/plugins/index.json` (the merged
   list of manifests) and `GET /api/v1/plugins/<name>/<file>` (any file from
   that plugin's ConfigMap). A ConfigMap can be created, updated, or deleted
   at any time — the backend picks up the change immediately, with no
   antrea-ui restart. Both routes are served with `Cache-Control: no-store`
   for exactly that reason. If a plugin's entry file is stored as
   gzip-compressed `binaryData` (worth doing once bundled with dependencies —
   detected by its magic number, no manifest changes needed), the backend
   passes it through as-is with `Content-Encoding: gzip` rather than
   decompressing and re-serving it.
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
relies on `@antrea/ui-plugin-sdk` to register itself with the host, and on
Antrea UI's REST API. Its own `vite.config.ts` must bundle dependencies like
`lit` in, rather than externalizing them (unlike `@antrea/ui-components`) —
there's no host-provided import map for a runtime `import()`.

The host passes your element no credential, and there is none to ask for:
requests to the Antrea UI backend authenticate with the `antrea-ui-session`
cookie the browser already holds, so `credentials: 'include'` is the whole of
it. (`apiFetch`/`apiFetchJSON` from `@antrea/ui-components` do this for you,
along with turning a non-2xx response into an `APIError`, if you would rather
not hand-roll it.) Never send an `Authorization` header of your own.

`plugins/examples/pod-counter/src/index.ts`:

```ts
import { LitElement, html } from 'lit';
import { state } from 'lit/decorators.js';
import { registerRoute, registerSidebarEntry } from '@antrea/ui-plugin-sdk';

class AntreaPluginPodCounter extends LitElement {
    @state() private _count: number | null = null;
    @state() private _error: string | null = null;

    connectedCallback() {
        super.connectedCallback();
        fetch('/api/v1/k8s/api/v1/pods', { credentials: 'include' })
            .then((res) => {
                if (res.status === 403) throw new Error('you do not have permission to list Pods');
                if (!res.ok) throw new Error(`request failed: ${res.status}`);
                return res.json();
            })
            .then((data) => { this._count = data.items?.length ?? 0; })
            .catch((e) => { this._error = e instanceof Error ? e.message : String(e); });
    }

    render() {
        if (this._error) return html`<p>Failed to load pod count: ${this._error}</p>`;
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

An entry can also nest under another top-level entry by setting `parentPath`
to that entry's `path` — a built-in page's (`flows`, `summary`, `traceflow`,
`settings`) or another plugin's own top-level entry's, with or without a
leading slash. The host renders it as a collapsible group (an
`antrea-nav-group`, from `@antrea/ui-components` — the same primitive the
host would use to nest, say, Flow List and Service Map under Flow Visibility)
instead of a flat item. Nesting is one level deep: pointing `parentPath` at
an entry that is itself nested is invalid, and the host drops the nesting
(falling back to a top-level entry) and logs why.

`summary` and `traceflow` are themselves gated by per-user RBAC (see
[authentication.md](authentication.md)) and simply don't render for a user
who fails that gate. Nesting under either still falls back to a top-level
entry for such a user, same as the invalid-nesting case above — but silently,
per user, and without dropping anything: the entry itself is always valid,
it just isn't always nested.

`@antrea/ui-plugin-sdk` is a devDependency resolved from this repo's
workspace (`file:../../../client/web/antrea-ui-plugin-sdk` in
`package.json`) — build it once before building any example plugin:

```bash
cd client/web/antrea-ui-plugin-sdk && yarn build
cd ../../../plugins/examples/pod-counter
npm install && npm run build   # vite build, then copies manifest.json into dist/
```

Your plugin can proxy any K8s API path: there is no allowlist in front of the
proxy, RBAC is the only guard (see
[`pkg/server/api/k8s.go`](../pkg/server/api/k8s.go)). What decides whether the
call succeeds is the RBAC of whoever is logged in — which is why the example
handles a 403 rather than assuming the call goes through. Before the backend
called Kubernetes as the end user, every UI user had the same access and that
branch was dead code; now it is the ordinary outcome for a user whose role does
not cover the path your plugin reads.

RBAC for that path is **not** added to Antrea UI's own `clusterroles.yaml`.
Instead, ship your own `ClusterRole` labeled
`rbac.ui.antrea.io/aggregate-to-antrea-ui-admin: "true"`, which
[aggregates](https://kubernetes.io/docs/reference/access-authn-authz/rbac/#aggregated-clusterroles)
into the `antrea-ui-admin` ClusterRole automatically — see
[`plugins/examples/pod-counter/clusterrole.yaml`](../plugins/examples/pod-counter/clusterrole.yaml).
Creating (and RBAC-scoping) that ClusterRole is the responsibility of
whoever deploys the plugin, not Antrea UI itself.

That aggregation only covers the admin-password mode, which impersonates the
`antrea-ui-admin` ServiceAccount. Users who log in with their own Kubernetes
identity (OIDC, kubeconfig, token) are bound to a role the cluster admin wrote,
which your ClusterRole does not aggregate into — so your plugin's pages will
403 for them until that admin grants the same permissions deliberately. Say so
in your plugin's install instructions. See
[authentication.md](authentication.md).

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
| `registerSidebarEntry` | Sidebar | Adds a nav entry linking to `entry.path`; nests under `entry.parentPath` if set. |
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
that `<namespace>` is wherever the backend watches for plugins —
`plugins.namespace` if set, otherwise Antrea UI's own release namespace (see
"How it works" above).

```bash
cd plugins/examples/pod-counter
npm install && npm run build

kubectl create configmap pod-counter-plugin -n <namespace> \
  --from-file=dist/index.js --from-file=dist/manifest.json
kubectl label configmap pod-counter-plugin -n <namespace> \
  ui.antrea.io/plugin=true
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
  ui.antrea.io/plugin=true
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
