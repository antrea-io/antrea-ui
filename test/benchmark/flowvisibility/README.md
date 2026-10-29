# Flow visibility benchmark

`flowbench` measures what the flow visibility pages, the Service Map in particular, cost the
antrea-ui backend and the browser, at a scale you choose.

It needs a Kubernetes cluster, but neither Antrea nor the Flow Aggregator: a fake Flow Aggregator
(`fakefa`) models a synthetic cluster (Namespaces, Nodes, workloads, Services and the connections
between them) and streams the flow records that cluster would produce, over the real
FlowStreamService gRPC API. antrea-ui runs unchanged, installed from the local chart with
`flowAggregator.enabled=true` and nothing else:

```text
cluster (kind is fine)
  flow-aggregator/flow-aggregator          kube-system/antrea-ui (unchanged chart)
  +----------------------------+  gRPC  +------------------------------+
  | fakefa: synthetic cluster  |------->| backend  <--  frontend nginx |
  +----------------------------+  TLS   +------------------------------+
                                                       ^
                                                       | HTTP + SSE, NodePort 31234
                                                       |
                         your browser, or flowbench's headless Chromium
```

`fakefa` is deployed where the real Flow Aggregator would be (`flow-aggregator.flow-aggregator.svc:14740`),
with its CA in the `flow-aggregator-ca` ConfigMap, so the chart's defaults find it. `flowbench`
refuses a cluster whose `flow-aggregator` Namespace it did not create itself. `fakefa` does not
authenticate or authorize callers, and discloses every endpoint in full: what a user allowed to
observe flows in every Namespace would get.

## Setup

From the repository root, with Docker, Helm, Go and Node.js:

```bash
# A cluster. Kind's default CNI is fine: no Antrea is needed.
kind create cluster --name flowbench

# Images: antrea-ui from the working tree, and the fake Flow Aggregator.
make build
docker build -t antrea/antrea-ui-fakefa:latest -f test/benchmark/flowvisibility/fakefa.dockerfile \
  --build-arg GO_VERSION=$(head -n 1 .go-version) .
kind load docker-image antrea/antrea-ui-frontend:latest antrea/antrea-ui-backend:latest \
  antrea/antrea-ui-fakefa:latest --name flowbench

# The headless browser, for "flowbench run" only. install-deps needs sudo (apt), once per machine.
(cd test/benchmark/flowvisibility/browser && npm ci && npx playwright install chromium \
  && sudo env PATH="$PATH" npx playwright install-deps chromium)

go build -o bin/flowbench ./test/benchmark/flowvisibility/cmd/flowbench
```

Every `flowbench` command takes `--context` (and `--kubeconfig`); they default to the current
context.

## Using the UI on a synthetic cluster

```bash
bin/flowbench deploy --context kind-flowbench --scale medium
```

This deploys `fakefa` with the requested scale, creates the synthetic Namespaces (so that the
Namespace pickers offer them), installs antrea-ui, and prints the URL and a 24h token for a
cluster-admin ServiceAccount to log in with. On a kind cluster the URL is the Node's address,
reachable from the machine running kind. To reach the UI from another machine, use the
`kubectl port-forward --address 0.0.0.0` command it prints.

Run `deploy` again with another scale to change it: `fakefa` restarts with the new one, and
antrea-ui reconnects to it. The page reconnects on its own too.

To watch the backend while you use the UI:

```bash
bin/flowbench monitor --context kind-flowbench
```

## Headless measurement

```bash
bin/flowbench run --context kind-flowbench --scale small,medium,large
```

For each scale, `run` deploys it, opens the Service Map (`--path /flows/list?namespace=_all` for
the flow list) in a headless Chromium logged in as the benchmark's cluster-admin, waits for the
first render, lets it warm up (`--warmup`, 30s), then samples for `--duration` (2m) every
`--sample-interval` (2s). Use `--no-deploy` to measure whatever is deployed instead.

Results go to `test/benchmark/flowvisibility/results/<timestamp>/<scale>/`:

| File | Content |
| --- | --- |
| `summary.md`, `summary.json` | The metrics below, summarized over the measurement |
| `page-samples.jsonl` | Raw samples from inside the page, from the first render on (`"phase": "warmup"` or `"measure"`) |
| `host-samples.jsonl` | Raw samples of the backend, `fakefa` and the browser processes |
| `scale.json` | The scale that was measured |
| `trace.json` | With `--trace-seconds N`: a Chrome performance trace of the first N seconds, to open in DevTools' Performance panel |

With several scales, `comparison.md` puts them side by side.

### What is measured

| Metric | Source |
| --- | --- |
| First render, settle time | Time to the map's first node (or the list's first flow); then time until the main thread is first under 20% busy, i.e. the initial layout has converged. Only during the warmup, so a long settle needs a long `--warmup` |
| Flows streamed to antrea-ui | `fakefa` `/stats`, read through the API server's Service proxy |
| Connections in the page's flow store, map nodes and edges, SVG elements | The page's DOM, and its flow store's size |
| Renderer main thread busy, and its script, layout and style shares | CDP `Performance.getMetrics` (`TaskDuration`, `ScriptDuration`, ...) |
| Frames per second, longest frame gap | A `requestAnimationFrame` counter injected before the page loads |
| Long tasks | A `longtask` `PerformanceObserver` |
| Event loop lag | How late a 50ms timer fires |
| JS heap, DOM nodes | CDP `Performance.getMetrics` |
| Browser CPU and RSS, by process type | `/proc`, for the Chromium processes `flowbench` started |
| Backend CPU, RSS, Go heap, goroutines | The backend's own `/metrics` (`process_*`, `go_*`) |

### Caveats

- Everything shares one machine: the kind Node (with the backend and `fakefa`), and the browser.
  Compare runs made on the same machine, not absolute numbers across machines.
- The backend is measured as a process: the frontend's nginx container, which proxies the SSE
  stream, is not included. Scraping `/metrics` every sample costs the backend a little CPU.
- The headless Chromium renders in software, so the GPU process's CPU stands for compositing work
  a real GPU would mostly take off the CPU.

## Scale

`--scale` picks a preset, and `--set knob=value` (repeatable) overrides any knob of it, e.g.
`--scale large --set peersPerWorkload=6 --set activeTimeout=30s`. `flowbench presets` lists the
presets and every knob with its default.

| Preset | Namespaces | Nodes | Pods/Node | Pods/workload | Workloads (map nodes) | Long-lived connections | Records/s |
| --- | --- | --- | --- | --- | --- | --- | --- |
| tiny | 3 | 2 | 10 | 2 | 10 | ~60 | ~3 |
| small | 10 | 5 | 20 | 3 | 33 | ~250 | ~14 |
| medium | 30 | 20 | 30 | 3 | 200 | ~1,900 | ~90 |
| large | 100 | 50 | 40 | 4 | 500 | ~6,000 | ~300 |
| xlarge | 300 | 200 | 50 | 5 | 2,000 | ~30,500 | ~1,500 |

Topology:

| Knob | Meaning |
| --- | --- |
| `namespaces` | Application Namespaces; workloads are spread evenly across them |
| `nodes`, `podsPerNode` | Pods are dealt to Nodes round-robin; Pods on the same Node talk intra-node |
| `podsPerWorkload` | Replicas per workload. A workload is one Service Map node |
| `serviceFraction` | Fraction of workloads behind a ClusterIP Service; only those can be peers |
| `peersPerWorkload` | Services each workload is a client of: the out-degree of a map node |
| `crossNamespaceFraction` | Fraction of those peers in another Namespace |
| `externalEgressFraction`, `externalIngressFraction`, `externalIPs` | Traffic to and from outside the cluster |
| `allowPolicyFraction`, `dropPolicyFraction` | Fraction of edges an allow or a drop rule applies to (edge colors) |

Traffic:

| Knob | Meaning |
| --- | --- |
| `connectionsPerPeer` | Long-lived connections each Pod keeps to each peer, each to a random backend Pod |
| `activeTimeout` | How often a long-lived connection is reported, like the Flow Aggregator's active flow timeout. Long-lived connections / `activeTimeout` is the steady record rate |
| `shortConnectionRate` | Short-lived connections per second, cluster-wide, one record each. Presets use one per Pod every 10s |
| `podChurnRate` | Pods replaced per minute: new name and IP, so their connections end and new ones start |
| `ringBufferSize` | Records kept for a new stream to replay |
| `prefillHistory` | Start with one `activeTimeout` of history, like a Flow Aggregator that has been running for a while |
| `maxBatchSize` | Records per GetFlowsResponse |
| `seed` | Makes the topology and traffic reproducible |

## Cleanup

```bash
bin/flowbench teardown --context kind-flowbench   # or: kind delete cluster --name flowbench
```
