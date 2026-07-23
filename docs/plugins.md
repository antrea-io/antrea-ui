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
`allowedK8sPaths` in [`pkg/server/api/k8s.go`](../pkg/server/api/k8s.go) and
grant matching RBAC in
[`clusterrole.yaml`](../build/charts/antrea-ui/templates/clusterrole.yaml) —
the one part of adding a plugin that isn't purely additive on the frontend.

**This grant is not scoped to the plugin** — Antrea UI has no per-user
permission model, so every path added to `allowedK8sPaths` becomes reachable
by any authenticated Antrea UI user, whether or not they use the plugin that
needed it. Only add paths/verbs your plugin actually requires, and be
mindful of what a cluster-wide `list`/`get` on that resource exposes (e.g.
`pods` list includes image references, env var names, and volume mounts
across every namespace).

## Trying it locally

Antrea UI only makes sense running against a real cluster, so test against
one directly rather than a standalone dev server.

**Recommended: mount it into the unmodified image**, via the chart's
`extraVolumes` / `frontend.extraVolumeMounts` values — no rebuild needed:

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

**To test the image-packaging pipeline itself** (what CI does — see
[`plugin-ci.yml`](../.github/workflows/plugin-ci.yml)):

```bash
cd plugins/examples/pod-counter
tar czf pod-counter.tgz -C dist .
cd ../../..

docker build -f build/frontend.dockerfile -t antrea-ui-frontend:ci .
docker build -f build/ci/frontend-with-example-plugin.dockerfile \
  --build-arg BASE_IMAGE=antrea-ui-frontend:ci -t antrea-ui-frontend:ci-with-plugin .
```

This bakes the plugin into the image rather than mounting it — only useful
for testing the build/ship workflow itself. Production deployments should
use the mounted-volume approach above.
