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

1. At container startup,
   [`plugin-index-builder.sh`](../build/scripts/plugin-index-builder.sh)
   scans `/etc/plugins/*/manifest.json` and merges them into
   `/etc/plugins/index.json`. Nginx serves `/etc/plugins` under `/plugins/`.
2. On load, Antrea UI fetches `/plugins/index.json` and `import()`s each
   plugin's JS module at runtime — the code doesn't need to exist when the
   frontend is built. See [`plugins.ts`](../client/web/antrea-ui/src/plugins.ts).
3. Each module registers a [Lit](https://lit.dev) custom element via
   `customElements.define(...)`, same as `@antrea/ui-components` does for
   Antrea UI's own pages.
4. A manifest with a `navItem` automatically gets a sidebar entry and route
   — no changes to Antrea UI's own source required.

## The manifest

```json
{
  "name": "pod-counter",
  "version": "0.1.0",
  "entry": "index.js",
  "tag": "antrea-plugin-pod-counter",
  "navItem": {
    "label": "Pod Counter",
    "path": "/plugin/pod-counter",
    "icon": "M7.752.066a.5.5 0 0 1 .496 0l3.75 2.143a.5.5..."
  }
}
```

| Field | Required | Description |
| --- | --- | --- |
| `name` | yes | Unique name; also the directory name under `/etc/plugins`. |
| `version` | yes | Informational only. |
| `entry` | yes | Plugin's JS module filename, relative to its directory. |
| `tag` | yes | Custom element tag name registered by `entry`. |
| `navItem` | no | Adds a sidebar entry + route. Omit for plugins with no page of their own. |
| `navItem.label` | if `navItem` set | Sidebar label. |
| `navItem.path` | if `navItem` set | Route path, e.g. `/plugin/pod-counter`. Must **not** start with `/plugins/` — that prefix is reserved for static plugin assets. |
| `navItem.icon` | no | SVG path `d` data, 16x16 (`viewBox="0 0 16 16"`), matching the built-in nav icons' style. |

## Writing a plugin

A plugin is a standalone package — not part of the `client/web` Yarn
workspace, and it doesn't depend on `@antrea/ui-components` internals. It
only relies on: the `token` property/attribute the host sets on its custom
element (for authenticated calls), and Antrea UI's REST API. Its own
`vite.config.ts` must bundle dependencies like `lit` in, rather than
externalizing them (unlike `@antrea/ui-components`) — there's no
host-provided import map for a runtime `import()`.

`plugins/examples/pod-counter/src/index.ts`:

```ts
import { LitElement, html } from 'lit';
import { property, state } from 'lit/decorators.js';

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
```

```bash
cd plugins/examples/pod-counter
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

## Trying it locally

Antrea UI only makes sense running against a real cluster, so test against
one directly rather than a standalone dev server.

**Recommended: mount it into the unmodified image**, via the chart's
`extraVolumes` / `frontend.extraVolumeMounts` values — no rebuild needed.
A ConfigMap is a simple way to populate the volume, but note ConfigMaps are
capped at 1MiB total:

```bash
cd plugins/examples/pod-counter
npm install && npm run build

kubectl create configmap pod-counter-plugin -n <namespace> \
  --from-file=dist/index.js --from-file=dist/manifest.json
```

Add to your Helm values (e.g. `plugin-volume-values.yaml`):

```yaml
extraVolumes:
  - name: pod-counter-plugin
    configMap:
      name: pod-counter-plugin
frontend:
  extraVolumeMounts:
    - name: pod-counter-plugin
      mountPath: /etc/plugins/pod-counter
      readOnly: true
```

```bash
helm upgrade antrea-ui build/charts/antrea-ui -n <namespace> \
  --reuse-values -f plugin-volume-values.yaml
```

The mount is in place before `plugin-index-builder.sh` runs, so
`/etc/plugins/index.json` picks it up automatically — no image rebuild, no
`kind load`, no tarball. To iterate on the plugin's code:

```bash
npm run build
kubectl delete configmap pod-counter-plugin -n <namespace>
kubectl create configmap pod-counter-plugin -n <namespace> \
  --from-file=dist/index.js --from-file=dist/manifest.json
helm upgrade antrea-ui build/charts/antrea-ui -n <namespace> \
  --reuse-values -f plugin-volume-values.yaml
```

The last `helm upgrade` is needed even with no value changes — the chart
stamps a fresh pod-recreating annotation on every render, so it forces the
new ConfigMap content to actually get mounted into a new pod.

This same setup — build the plugin, mount it via a ConfigMap and
`extraVolumes`/`frontend.extraVolumeMounts`, install Antrea UI — is what
[`test/e2e/plugin_test.go`](../test/e2e/plugin_test.go) automates as a real
e2e test in [`kind_e2e.yml`](../.github/workflows/kind_e2e.yml), against an
actual Kind cluster rather than a standalone container.

## Future work

* **Deeper customization.** Today a plugin can only add a whole new page.
  Modifying an *existing* UI element (e.g. adding a column to the flow
  table) would need Antrea UI to expose `registerX`-style extension points,
  likely via a small shared library package plugins depend on. Not
  implemented yet.
* **Backend-hosted plugin serving.** `plugin-index-builder.sh` merges
  manifests once at container startup. A plugin installed afterwards isn't
  picked up until the frontend restarts. Moving plugin discovery/serving
  into the Go backend (as an unauthenticated API, like `/api/v1/settings`)
  would let it watch for changes — e.g. plugins delivered as labeled
  ConfigMaps in a well-known namespace — and make new plugins show up on a
  browser refresh, with no Antrea UI restart required.
