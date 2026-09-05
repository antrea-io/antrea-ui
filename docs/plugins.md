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

A plugin bundle can come from either of two sources, watched at the same
time: a labeled Kubernetes `ConfigMap`, or a subdirectory of a filesystem
directory the backend is pointed at (`plugins.directory`). Both are
described below; if a plugin name is delivered by both, the ConfigMap wins
and the directory copy is dropped (and logged).

1. A plugin is delivered as a Kubernetes `ConfigMap`, in the namespace the
   backend watches for plugins (`plugins.namespace`, default: antrea-ui's own
   release namespace), labeled to match `plugins.labelSelector` (default:
   `ui.antrea.io/plugin=true`). Its `data` holds `manifest.json` (small and
   human-readable, so `kubectl get configmap -o yaml` shows it directly);
   everything else the manifest references — the entry file, and for a
   federation remote, `remoteEntry` plus every file it names — is zipped
   into one `bundle.zip` key under `binaryData`.

   A single archive rather than one ConfigMap key per file for two reasons:
   a `data`/`binaryData` key name can't contain `/` at all (rejected by the
   apiserver), so a plugin with subdirectory-nested assets (e.g. Angular's
   `assets/` convention — images, i18n locale files, anything referenced by
   a relative runtime URL rather than pulled into the JS module graph)
   couldn't be represented as flat keys; and validating/serving one archive
   is simpler than juggling an arbitrary key set. See "The manifest" below
   for the exact layout.

   The RBAC this requires (`get`/`list`/`watch` on ConfigMaps) is granted on
   every ConfigMap in that namespace, not just labeled plugin ones — the
   label selector is applied by the backend's watch, not by RBAC. Since
   antrea-ui is commonly installed into `kube-system`, which can host other,
   unrelated, more sensitive ConfigMaps, set `plugins.namespace` to a
   dedicated namespace if you want plugin ConfigMaps isolated from it. If
   that namespace differs from the release namespace, whoever runs
   `helm install`/`upgrade` needs permission to create a `Role`/`RoleBinding`
   there too — the chart can't grant permissions outside its own release
   namespace. `plugins.maxConfigMapPlugins` (default 10) caps how many
   distinct ConfigMap-backed plugins the backend will track at once — a new
   plugin past the cap is rejected and logged, though updates to an
   already-tracked one are never blocked by it. `plugins.maxBundleBytes`
   (default 10MiB, shared with the directory source below) separately caps
   how much a single plugin's `bundle.zip` may decompress to in total, once
   extracted to disk (see below): the ConfigMap's own ~1MiB etcd size limit
   only bounds the compressed bytes, not what they decompress to, so
   without this a small, maliciously high-ratio archive (a "zip bomb")
   could still exhaust the backend's disk. A `ConfigMap` update that
   doesn't actually change its contents (same
   `resourceVersion` — most commonly the informer replaying its cache after
   a watch reconnect) is a no-op: the backend skips re-extracting a
   `bundle.zip` it's already extracted.

   Alternatively, a plugin can be delivered from a plain filesystem
   directory instead of a `ConfigMap`: set `plugins.directory` (or the
   `ANTREA_UI_PLUGINS_DIRECTORY` env var) to a path, and put each plugin in
   its own immediate subdirectory there, e.g.
   `<plugins.directory>/pod-counter/manifest.json` and
   `<plugins.directory>/pod-counter/bundle.zip` — the same manifest.json +
   bundle.zip layout as the ConfigMap source, just as two files instead of
   a `data`/`binaryData` key each. This needs no RBAC and no cluster
   round-trip, which is why it's the easiest way to iterate on a plugin
   locally (see "Trying it locally" below); it's also there as a fallback
   if a deployment's plugins outgrow a `ConfigMap`'s 1MiB cap, backed by
   whatever shared volume the deployment wires up between the backend Pod
   and whoever writes the plugin bundle there - the Helm chart's
   `plugins.directory` value sets the path, while the volume itself (a
   `hostPath`, a `PVC`, ...) is mounted via the chart's generic
   `extraVolumes`/`backend.extraVolumeMounts` values. `plugins.maxDirectoryPlugins`
   (default 10) caps this source the same way `plugins.maxConfigMapPlugins`
   caps the ConfigMap one, with one difference: a directory plugin rejected
   for being past the cap is only retried on its own next filesystem event,
   so it can stay rejected indefinitely after capacity frees up elsewhere
   (e.g. another plugin's directory is removed) unless that plugin's own
   files change, or the whole watched directory disappears and comes back
   — a ConfigMap past the cap doesn't have this gap, since a watch
   reconnect relists every ConfigMap and retries it regardless. A plugin
   name can only come from one source at a
   time — if both a `ConfigMap` and a directory declare the same `name`,
   the `ConfigMap` wins and the directory copy is dropped (and logged).
2. The Go backend watches ConfigMaps matching that label, and the
   `plugins.directory` path if set (see [`pkg/plugins`](../pkg/plugins) and
   [`pkg/server/api/plugins.go`](../pkg/server/api/plugins.go)), and serves
   the merged result, unauthenticated, at `GET /api/v1/plugins/index.json`
   (the merged list of manifests) and `GET /api/v1/plugins/<name>/<file>`
   (any file inside that plugin's `bundle.zip`, from either source — `file`
   may itself contain `/`, e.g. `assets/logo.png`). Either source can be
   created, updated, or deleted at any time — the backend picks up the
   change immediately, with no antrea-ui restart. Both routes are served
   with `Cache-Control: no-store` for exactly that reason. If a plugin's
   entry file is itself already gzip-compressed inside `bundle.zip` (worth
   doing once bundled with dependencies — detected by its magic number, no
   manifest changes needed), the backend passes it through as-is with
   `Content-Encoding: gzip` rather than decompressing and re-serving it.

   Both sources extract each plugin's `bundle.zip` to a local scratch
   directory (under the backend's `/tmp`) and serve files straight from
   there, rather than holding them in memory — decoding an entire
   (potentially large) bundle into memory on every reload would otherwise
   be an unbounded cost, and it's what lets both sources share the same
   extraction, size-limit, and
   path-traversal logic. This means **the backend needs a writable `/tmp`**
   for plugins to work at all, ConfigMap-sourced ones included — if you run
   the backend with `readOnlyRootFilesystem: true`, mount a small `emptyDir`
   at `/tmp` yourself via the chart's generic
   `extraVolumes`/`backend.extraVolumeMounts` values. A plugin's bundle is
   extracted to a temporary directory first, then that directory is
   renamed into its final place, so a request racing a reload never sees
   a partially-extracted plugin — but since a rename can't atomically
   replace an existing, non-empty directory, the final directory is
   removed just before the rename, leaving a brief window where it
   doesn't exist at all; a request racing a reload in that window gets a
   404 rather than either version. The directory source's own extraction is
   bounded by that same `plugins.maxBundleBytes` — a plugin directory
   carries about as much trust as a plugin ConfigMap, so there's no reason
   for the two to have separate limits, and a plugin directory has no
   equivalent to a `ConfigMap`'s etcd size cap at all, making this the only
   thing bounding its decompressed size (it does not cap the number of
   entries in a bundle). Both the extraction step and file serving clamp a
   `../`-style path (in an archive entry name, or in a
   request path) into the plugin's own directory instead of rejecting it
   outright — the same technique `net/http.Dir` uses — so it can never
   escape that directory, but it does still land somewhere inside it
   rather than being refused.
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

### Upgrading an existing plugin ConfigMap

The single-`bundle.zip` layout above replaces an older one where every file a
plugin shipped was its own `data`/`binaryData` key, named after its path
inside the bundle. A ConfigMap built for that older layout has no
`bundle.zip` key at all, so after upgrading antrea-ui, the backend logs
`missing bundle.zip` for it and stops serving that plugin — there is no
automatic conversion. Rebuild the ConfigMap with the new layout (see "The
manifest" below and
[`plugins/examples/pod-counter`](../plugins/examples/pod-counter)): a
`manifest.json` `data` key plus a `bundle.zip` `binaryData` key holding
every other file the manifest references, zipped.

## The manifest

`manifest.json` is delivered on its own (a ConfigMap `data` key, or a file
next to `bundle.zip` in a plugin directory) — everything else it references
lives inside `bundle.zip`:

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
| `entry` | yes | Plugin's JS module filename; must be an entry in the same plugin's `bundle.zip`. Always eagerly `import()`-ed by the host at startup, for whatever page-extension registration the plugin's code performs (see below) — independent of `federation`. Required even for a plugin whose only page(s) are a `federation` remote with no other page-extension registration; such a plugin still needs a real ES module here, distinct from `federation.remoteEntry` — see below. |
| `federation` | no | `{remoteEntry, routes: [{path, sidebarLabel, icon?, exposedModule, kind?}]}` — a [Native Federation](https://www.npmjs.com/package/@angular-architects/native-federation) remote (its own entry in `bundle.zip`, separate from `entry`) plus the whole-page routes/sidebar entries it serves, as data instead of registering them in code (see below). Antrea UI's own frontend has no module federation loader and ignores this field entirely (see `plugins.ts`); it's consumed by a separate, out-of-tree Angular-based host, which lazily loads a route's `exposedModule` out of `remoteEntry`, only once that route is actually visited. `kind` is `"component"` (the default) or `"routes"` — any other value is rejected, dropping the whole plugin (see below): `"component"` expects `exposedModule` to export a single page component; `"routes"` expects it to export a whole route tree the plugin owns end to end, letting it nest its own sub-paths and register its own route-level providers without the host knowing anything about them. Since a `"routes"` route owns every sub-path under its own `path`, no other route in the same manifest may fall under it (rejected the same way two routes with an identical `path` are); a route nested under it in a *different*, already-installed plugin's manifest is resolved the same way an identical `path` across plugins is (see below). |

`bundle.zip`'s own internal layout is entirely up to the plugin — a flat set
of files, or nested paths like `assets/logo.png` for anything referenced by
a relative runtime URL rather than pulled into the JS module graph (images,
i18n locale files, etc.) — the backend extracts/serves whatever structure
it finds, using each entry's own path (including any `/`) as the filename
in `GET /api/v1/plugins/<name>/<file>`.

For most plugins, that's the whole schema: the manifest only carries enough
for the host to fetch and `import()` the right file, and everything that
affects the UI (routes, sidebar entries, page extensions) is registered in
code, via `@antrea/ui-plugin-sdk`, so a plugin's actual shape is never split
between JSON and JS. `federation` is the one exception, for a plugin whose
page(s) are a module federation remote — deferring code execution until a
route is visited requires the host to know the route beforehand, which
requires it to be data rather than something only running the plugin's code
would reveal.

A manifest declaring `federation`:

```json
{
  "name": "policy-management",
  "version": "0.2.0",
  "entry": "index.js",
  "federation": {
    "remoteEntry": "remoteEntry.json",
    "routes": [
      {
        "path": "/policies",
        "sidebarLabel": "Policy Management",
        "icon": "M0 0h16v16H0z",
        "exposedModule": "./PolicyManagementPage"
      }
    ]
  }
}
```

The backend refuses to load a plugin (logging why) if any of the following
don't hold — each is a case that would otherwise surface only as a broken
route or a silent skip once the plugin is already installed. A ConfigMap
that fails these checks on an update, rather than on first load, leaves the
previously loaded version in place until it's fixed or deleted; a plugin
directory does not have this fallback — an update that fails these checks
stops the plugin from being served at all until a subsequent update fixes
it:

- `federation.remoteEntry` must be an entry in the same plugin's
  `bundle.zip`, and must not be the same entry as `entry` — the host
  always `import()`s `entry` as a plain ES module, which a federation
  remote entry is not.
- `federation.routes` must be non-empty — a remote with nothing to mount is
  meaningless on its own.
- Each route needs `path`, `sidebarLabel`, and `exposedModule`.
- `path` may not be the root path (`/`) or fall under a path nginx proxies
  straight to the backend (currently `api`, `auth`, e.g. `/api/v1/foo` or
  `/authors`) — the former collides with the host's own home page, the
  latter would install and navigate fine client-side, then 404 on a hard
  refresh or a direct link.
- `path` may not duplicate another route's `path` in the same manifest
  (leading/trailing/doubled slashes, and `.`/`..` segments, ignored when
  comparing).
- `kind`, if set, must be `"component"` or `"routes"` — anything else drops
  the whole plugin, not just the one route: every route plus `entry` is
  unloaded (or, on an update, the previously loaded version is silently kept
  in place), the same as any other manifest-level rejection in this list.

A route `path` colliding with another, already-installed plugin's route
`path` — or falling under another plugin's `"routes"`-kind route, which owns
its whole sub-tree just as it does within a single manifest — is a separate,
softer case, resolved once every ConfigMap/directory plugin is already
loaded rather than at parse time: whichever plugin's source sorts first
(ConfigMap before directory; by ConfigMap/directory name within a source —
the same order "How it works" describes for a plugin *name* collision)
keeps whatever it validly claimed. Which route is dropped depends on which
side sorts first: if the plugin declaring the `"routes"`-kind route sorts
first, the later plugin just loses the one route nested under it (and its
sidebar entry); if a plugin already claiming a path under that sub-tree
sorts first instead, the later plugin loses its whole `"routes"`-kind route
rather than just the nested one, since keeping it would let it claim a path
another plugin already owns. Either way, the losing plugin only loses that one
route (and its sidebar entry) from `GET /api/v1/plugins/index.json` — the
rest of its manifest, including `entry`, is unaffected. Only if every one
of a plugin's routes collides is the whole plugin dropped from the index,
the same resolution as two plugins declaring the same `name`.

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
npm install && npm run build   # vite build, copies manifest.json, zips everything else into bundle.zip
```

The `build` script's `bundle.zip` step shells out to the `zip` binary, which
needs to be installed separately — it isn't an npm dependency. Without it,
`npm run build` fails with `zip: command not found`.

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
one directly rather than a standalone dev server. Everything below is about
the plugin bundle itself, not the cluster — the fastest way to iterate is
`plugins.directory`, not a `ConfigMap`:

```bash
cd plugins/examples/pod-counter
npm install && npm run build   # vite build, copies manifest.json, zips everything else into bundle.zip
mkdir -p <plugins.directory>/pod-counter
cp dist/manifest.json dist/bundle.zip <plugins.directory>/pod-counter/
```

The backend's directory watch picks this up immediately, no restart needed
— refresh the browser and the plugin's page shows up. To iterate, just
rebuild and `cp` again; to remove it, delete
`<plugins.directory>/pod-counter`. RBAC still applies (see below), so
`kubectl apply -f clusterrole.yaml` once against whatever cluster the
backend is pointed at.

The rest of this section instead delivers the same plugin as a `ConfigMap`,
useful for testing that path specifically (e.g. before a real deployment, or
in the e2e test below). The commands below assume `@antrea/ui-plugin-sdk` is
already built (see "Writing a plugin" above), and that `<namespace>` is
wherever the backend watches for plugins — `plugins.namespace` if set,
otherwise Antrea UI's own release namespace (see "How it works" above).

```bash
cd plugins/examples/pod-counter
npm install && npm run build

kubectl create configmap pod-counter-plugin -n <namespace> \
  --from-file=dist/manifest.json --from-file=dist/bundle.zip
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
  --from-file=dist/manifest.json --from-file=dist/bundle.zip
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
