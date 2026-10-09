# Metrics

Antrea UI can read the Prometheus metrics of the Antrea components live: a
client opens a *tap* on one or more targets, picks the metrics it wants, and
the backend scrapes them on a fixed cadence and streams the samples for as
long as the tap stays open.

There is no storage. The backend keeps no history, a tap lives exactly as long
as its HTTP stream, and nothing is persisted: it is up to the client to buffer
what it wants to plot. This is a way to look at what a component is doing right
now, not a replacement for a Prometheus server.

The backend also exposes [its own metrics](#the-backends-own-metrics) for a
Prometheus server to scrape.

This document describes the backend API. Everything here is controlled by one
setting, `metrics.enabled`, which is on by default.

- [What can be tapped](#what-can-be-tapped)
- [Granting access](#granting-access)
- [API](#api)
- [Limits](#limits)
- [Trust model](#trust-model)
- [The backend's own metrics](#the-backends-own-metrics)
- [Configuration](#configuration)

## What can be tapped

Metrics come from *targets*: a target is one thing which can be scraped, for
example the Antrea Agent of one Node. Targets belong to *target groups*, one
for each Antrea component:

| Target group | Targets | Target ID |
| --- | --- | --- |
| `antrea-controller` | The Antrea Controller | `antrea-controller` |
| `antrea-agent` | One per Node running an Antrea Agent | `antrea-agent/<node>` |
| `antrea-ui` | The Antrea UI backend itself | `antrea-ui` |
| `flow-aggregator` | None yet | |

A target group is the unit of access and of discovery: access is granted for a
whole target group (see [Granting access](#granting-access)), and Antrea UI
finds and scrapes all its targets the same way. A target group can hold a
single target, whose ID is then the name of the target group. The targets of a
target group usually expose the same metrics, but nothing requires it: agents
of different versions, or on different operating systems, can differ.

The Flow Aggregator is listed, and reported as unavailable: its metrics cannot
be scraped by Antrea UI yet. The set of target groups is fixed.

## Granting access

A user can tap the targets of a target group when they hold the `get` verb on
the virtual resource `metrics.ui.antrea.io`, with the name of the target group
as the resource name, cluster-wide. `antrea-ui-admin-core` grants it for every
target group, so the admin password and any user bound to that role can tap
everything.

To give some users some target groups only, use `resourceNames` in a
ClusterRole of your own:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: antrea-controller-metrics-viewer
rules:
  - apiGroups: ["ui.antrea.io"]
    resources: ["metrics"]
    resourceNames: ["antrea-controller"]
    verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: antrea-controller-metrics-viewers
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: antrea-controller-metrics-viewer
subjects:
  - apiGroup: rbac.authorization.k8s.io
    kind: Group
    name: network-oncall
```

A RoleBinding does not work: the check is made without a namespace. No API
server serves this resource, so the rule gives access to nothing outside of
Antrea UI. See [Trust model](#trust-model) for who enforces it and why.

## API

All routes are under `/api/v1/metrics` and require authentication, like the
rest of the API. `GET /api/v1/settings` reports whether they are available, as
`features.metricsEnabled`.

| Route | Purpose |
| --- | --- |
| `GET /targets` | The target groups and their current targets |
| `GET /families?target=<id>` | What one target exposes |
| `POST /taps` | Open a tap. The response is the stream |
| `PUT /taps/<id>/selections` | Replace the selection of an open tap |

An error before a stream starts is a JSON string with a message for a human,
like everywhere else in the API. The one exception is the 429 of the rate
limit on the first three routes (see [Limits](#limits)), which has no body: on
`POST /taps`, this is what tells it from the 429 of the caps on open taps,
which has a message.

### `GET /api/v1/metrics/targets`

Lists what can be tapped right now. It takes no parameter and contacts no
target. Antrea UI watches the agents of the cluster, so the list follows Nodes
joining and leaving. Until it has a first view of them, shortly after it
starts, the `antrea-agent` target group has `"available": false`.

```json
{
  "targetGroups": [
    {
      "name": "antrea-controller",
      "allowed": true,
      "available": true,
      "targets": [{"id": "antrea-controller"}]
    },
    {
      "name": "antrea-agent",
      "allowed": true,
      "available": true,
      "targets": [
        {"id": "antrea-agent/kind-control-plane", "labels": {"node": "kind-control-plane"}},
        {"id": "antrea-agent/kind-worker", "labels": {"node": "kind-worker"}}
      ]
    },
    {
      "name": "antrea-ui",
      "allowed": true,
      "available": true,
      "targets": [{"id": "antrea-ui"}]
    },
    {
      "name": "flow-aggregator",
      "allowed": true,
      "available": false,
      "reason": "Flow Aggregator metrics are not supported yet",
      "targets": []
    }
  ]
}
```

One target group never fails the list:

- A target group the caller may not read has `"allowed": false` and no
  targets. Nothing is discovered for it.
- A target group whose targets cannot be listed has `"available": false` and
  a `reason`, for example `the Antrea agents are not known yet`.

The route is rate limited per user, together with `GET /families` and
`POST /taps`: a request over the limit is a 429. See [Limits](#limits).

### `GET /api/v1/metrics/families?target=<id>`

Scrapes one target once and returns the catalog of what it exposes, sorted by
name and without sample values. This is how a client picks the metrics of a
tap. The targets of a target group usually expose the same families, so a
client can ask one agent and then tap many. A tap leaves out a family which a
target does not expose.

```json
{
  "target": "antrea-agent/kind-worker",
  "timestamp": "2026-10-08T17:42:10.318Z",
  "families": [
    {
      "name": "antrea_agent_ovs_flow_count",
      "type": "gauge",
      "help": "Flow count for each OVS flow table. The TableID and TableName are used as labels.",
      "labelNames": ["table_id", "table_name"],
      "seriesCount": 34
    },
    {
      "name": "antrea_agent_ovs_flow_ops_latency_milliseconds",
      "type": "histogram",
      "help": "The latency of OVS flow operations, partitioned by operation type (add, modify and delete).",
      "labelNames": ["operation"],
      "seriesCount": 3
    }
  ]
}
```

`type` is `counter`, `gauge`, `histogram`, `summary` or `untyped`. `seriesCount`
counts the label sets of the family as exposed: a histogram with 3 values of
`operation` counts 3, not its bucket lines.

| Status | When |
| --- | --- |
| 501 | Metrics are disabled |
| 429 | Too many requests from this user: see [Limits](#limits) |
| 400 | `target` is missing, repeated or malformed |
| 404 | The target group does not exist |
| 403 | The caller may not read the target group |
| 501 | The target group exists but cannot be scraped (`flow-aggregator`) |
| 404 | There is no agent on that Node |
| 502 | The scrape failed |

The checks are made in this order, so an unknown target group is a 404 whoever
asks. The message of a 502 names the class of the failure (the target could not
be reached, the TLS handshake failed, ...) and never the underlying error,
which is in the backend log.

### `POST /api/v1/metrics/taps`

Opens a tap. The body names the scrape interval and, for each target, the
metric families to read:

```json
{
  "interval": "10s",
  "selections": [
    {"target": "antrea-agent/kind-worker", "metrics": ["antrea_agent_ovs_flow_count"]}
  ]
}
```

- `interval` is a Go duration. It must be between `metrics.minScrapeInterval`
  and `5m`, and defaults to `10s`, or to `metrics.minScrapeInterval` when that
  is longer.
- `selections` names at least one target: a tap always selects something. A
  target can appear once, and each selection must name at least one metric.
- A selection can name an agent whose Node does not exist. Nodes come and go,
  so this is not an error of the request: the tap reports it on every tick
  until the Node shows up.

The response is a `text/event-stream`. The first tick is immediate: the tap
does not wait for a first interval.

```text
event:tap
data:{"id":"9f2c41d07a3b4e18a6c05d7e91b2f344","interval":"10s","selections":[{"target":"antrea-agent/kind-worker","metrics":["antrea_agent_ovs_flow_count"]}]}

event:scrape
data:{"target":"antrea-agent/kind-worker","timestamp":"2026-10-08T17:43:00.004Z","families":[{"name":"antrea_agent_ovs_flow_count","type":"gauge","samples":[{"labels":{"table_id":"0","table_name":"PipelineRootClassifier"},"value":"5"},{"labels":{"table_id":"1","table_name":"ARPSpoofGuard"},"value":"12"}]}]}

: keepalive
```

| Event | Payload | Meaning |
| --- | --- | --- |
| `tap` | `{id, interval, selections}` | Sent first, and again when an update of the selection takes effect |
| `scrape` | `{target, timestamp, families, truncated}` | The selected families of one target, from one scrape. At most one per target per tick |
| `scrape_error` | `{target, timestamp, code, message}` | One target could not be scraped on this tick. The tap goes on |
| `error` | `{code, message, retryable}` | The tap is over. The stream ends after this event |

About `scrape` events:

- `timestamp` is when the scrape was actually performed.
- `families` holds `{name, type, samples}` for each selected family the target
  exposes, in the order of the selection. A family the target does not expose
  is left out.
- A sample is `{name, labels, value}`. Histograms and summaries are flattened
  to the series Prometheus would store, and `name` is only present when it
  differs from the name of the family: `<family>_bucket` (with an `le` label),
  `<family>_sum` and `<family>_count`. The quantiles of a summary carry the
  name of the family and a `quantile` label.
- `value` is a string, as in the Prometheus HTTP API, because `NaN` (a summary
  quantile with no observation) and `+Inf` are ordinary values which a JSON
  number cannot carry.
- `truncated` is `true` when the event reached the cap on samples per event and
  some were left out. It is absent otherwise.
- The events of the targets of one tick are sent as each scrape completes, in
  no particular order.

The `code` of a `scrape_error` is one of:

| Code | Meaning |
| --- | --- |
| `not_found` | The target does not exist, for example no agent on that Node |
| `unavailable` | The targets of the target group could not be resolved |
| `invalid_target` | The cluster reports nothing usable to reach the target safely: no CA bundle, no usable address |
| `credential_error` | Antrea UI could not obtain the credential it scrapes with |
| `unreachable` | The connection could not be established |
| `tls_error` | The TLS handshake failed, including a certificate the expected CA does not vouch for |
| `timeout` | The scrape did not complete in time |
| `bad_status` | The target answered with a status other than 200 |
| `response_too_large` | The response exceeded the size limit |
| `bad_response` | The response is not valid Prometheus metrics |
| `scrape_failed` | Anything else |

The `code` of a terminal `error` is one of:

| Code | Retryable | Meaning |
| --- | --- | --- |
| `forbidden` | No | The caller is no longer allowed to read one of the selected target groups |
| `unauthenticated` | No | The credential of the caller was rejected. The session is over |
| `authorization_unavailable` | Yes | Access could not be verified for several minutes in a row |

The stream also ends, without an `error` event, when the session of the caller
ends (logout, expiry). A comment line, `: keepalive`, is sent every 5 seconds
when there is nothing else to send. An open tap keeps its session alive, in a
background tab too: see [authentication.md](authentication.md#sessions).

Errors before the stream starts:

| Status | When |
| --- | --- |
| 501 | Metrics are disabled |
| 400 | The body is malformed, the interval is out of range, there are too many targets or metrics, a target is malformed or names a target group which does not exist or cannot be scraped |
| 403 | The caller may not read one of the selected target groups |
| 429 | Too many taps are open, in total or for this user, or too many requests from this user: see [Limits](#limits) |

### `PUT /api/v1/metrics/taps/<id>/selections`

Replaces the whole selection of an open tap, with the `id` its `tap` event
gave:

```json
{
  "selections": [
    {"target": "antrea-agent/kind-worker", "metrics": ["antrea_agent_ovs_flow_count", "antrea_agent_local_pod_count"]},
    {"target": "antrea-controller", "metrics": ["antrea_controller_network_policy_processed"]}
  ]
}
```

The answer is a 204. The stream stays open, and the tap keeps its ID and its
cadence, so a client's buffer continues uninterrupted. The stream carries a new
`tap` event with the new selection, which marks where it takes effect:
everything after that event belongs to the new selection, starting with the
next tick. Only the last update counts: when several are accepted before the
tap gets to them, which takes it up to 10s when a target does not answer, there
is one `tap` event, with the selection of the last one. The new selection
cannot be empty: a client which has nothing left to read closes the stream.

Only the caller who opened a tap can update it: with the same session, or for a
client which authenticates with a bearer token, as the same user.

| Status | When |
| --- | --- |
| 501 | Metrics are disabled |
| 404 | The tap is gone, or is not the caller's. Checked before anything else |
| 400 | The body or the selection is invalid, as for `POST /taps` |
| 403 | The caller may not read one of the target groups of the new selection |

## Limits

| Limit | Value | Setting |
| --- | --- | --- |
| Scrape interval of a tap | Between 5s and 5m, 10s by default (the lower bound, when it is set above 10s) | `metrics.minScrapeInterval` for the lower bound |
| Open taps, in total | 50 | `metrics.maxTaps` |
| Open taps, per user (per session with the admin password) | 5 | |
| Requests to `GET /targets`, `GET /families` and `POST /taps` together, per user (per session with the admin password) | 2 per second, with a burst of 10 | `metrics.maxRequestsPerSecond` for the rate |
| Targets per tap | 20 | `metrics.maxTargetsPerTap` |
| Metric families per target in a selection | 100 | |
| Samples per `scrape` event | 5000 | |
| Duration of a scrape | 10s, and a tap does not wait for one target longer than its own interval | |
| Size of the response of a target | 10MiB | |

Everyone who logs in with the admin password is the same user, so each of
these sessions has its own allowance for the two limits per user. Only
`metrics.maxTaps` bounds the taps which they have open together: whoever holds
the admin password can use up the taps of all users by logging in several
times.

One tap covers at most `metrics.maxTargetsPerTap` targets, so with the default
it cannot cover every agent of a cluster with more than 20 Nodes.

A tap asks for all its targets at once on every tick, and a target which does
not answer holds up no other one, in that tap or in another: the tap reports
it with a `scrape_error` once it has waited for it, and the tick is over. There
is no limit on the scrapes in flight as such. A target is scraped once at a
time, whoever asks, and only an agent which exists is contacted, so there are
never more of them than there are targets in the cluster.

Scrapes are shared between taps. Taps which read the same target at about the
same time get the result of one scrape, and a result is reused for half of
`metrics.minScrapeInterval`. Whatever the number of open taps, the backend
therefore scrapes a target at most once per half minimum interval. A tap never
sends the same result twice: a tick which comes so soon after a late one that
it gets the result the tap has already sent for a target sends nothing for
that target. This is why `timestamp` is the time of the scrape and not the
time of the tick.

## Trust model

**Who is authorized.** The user, with Kubernetes RBAC: `get` on
`metrics.ui.antrea.io/<target group>`, checked by Antrea UI with a
`SelfSubjectAccessReview` made with the user's own credential. It is checked
when a tap is opened, when its selection is updated, and every minute for as
long as it is open: a tap ends with a `forbidden` error at the first check
after the grant is removed. A check which is due waits for the tick in
progress, which lasts 10s at most, and comes before the scrapes of the next
one. It is not made while the client does not read its stream, during which
the tap scrapes nothing either. If the check itself fails, the tap keeps the
last answer for up to three checks in a row and is then closed with a
retryable error.

**Who scrapes.** Antrea UI, as the `antrea-ui-metrics-scraper` ServiceAccount,
with tokens it mints for 10 minutes at a time. That ServiceAccount can `get` the
`/metrics` non-resource URL, which is what the Antrea components require, and
nothing else. No user credential is ever sent to a target. The `antrea-ui`
target is not scraped at all: the backend reads its own registry.

This makes Antrea UI the enforcement point for these metrics, which it is for
nothing else: the targets see the scraper and cannot tell users apart. The
exposure is bounded because the targets are fixed. A user chooses among the
targets Antrea UI lists and cannot make it scrape an arbitrary address.

**Why not the user's credential.** The metrics of an agent are served by the
agent API, on an address of its Node, with a certificate each agent signs for
itself. The port and the CA bundle come from the agent's `AntreaAgentInfo` and
the address from its Node, and upstream RBAC lets every agent update any
`AntreaAgentInfo` and patch the status of any Node. Neither the CA bundle nor
the address is therefore a trust boundary: a compromised agent can make Antrea
UI connect to a server of its choice, with a certificate of its choice, for any
Node. A user's token sent there could be replayed against the kube-apiserver
with everything that user can do. `antctl` stopped forwarding the caller's
credentials to agents for the same reason.

What a compromised agent gains is what is left:

- It can capture a scraper token, which reads `/metrics` on the Antrea
  components, on the kube-apiserver and on anything else which authorizes
  `/metrics` the same way, plus whatever the cluster grants to every
  authenticated user or ServiceAccount. It gives access to no resource. An
  attacker who is root on a Node already holds stronger credentials.
- It can serve made-up metrics for any agent, or make its scrapes fail.
- It can make Antrea UI open a TLS connection to an address of its choice.
  Loopback, link-local and unspecified addresses are refused. The token is
  only sent once the handshake has succeeded against the CA bundle of the
  target, and redirects are never followed.

An agent which reports no CA bundle is not scraped: there is no fallback to an
unverified connection.

**What the backend holds.** With `metrics.enabled`, the `antrea-ui`
ServiceAccount can mint tokens for `antrea-ui-metrics-scraper`, and `list` and
`watch` AntreaAgentInfos and Nodes. It watches both for as long as it runs, and
keeps the name, API port and CA bundle of each agent and the name and addresses
of each Node in memory: which agents a user lists or selects, and how often,
then costs the kube-apiserver nothing. The scraper ServiceAccount can also be
assumed by anyone who can create Pods or ServiceAccount tokens in the Namespace
of the release. That adds nothing in `kube-system`, where such a user can do
far more already, and is worth knowing if you install Antrea UI elsewhere.

The Antrea Controller is reached through the `antrea` Service and verified
against the `antrea-ca` ConfigMap, like every other request Antrea UI makes to
it.

## The backend's own metrics

With `metrics.enabled`, the backend serves its own metrics in the Prometheus
text format at `GET /metrics`, on the backend port and, through the frontend
container, on the Service port (with TLS when `https.enable` is set). This is
separate from the tap API: it is meant for a Prometheus server.

| Metric | Type | Labels |
| --- | --- | --- |
| `antrea_ui_build_info` | Gauge | `version`, `goversion` |
| `antrea_ui_http_requests_total` | Counter | `method`, `path`, `code` |
| `antrea_ui_http_request_duration_seconds` | Histogram | `method`, `path` |
| `antrea_ui_metrics_taps_open` | Gauge | |
| `antrea_ui_metrics_tap_scrapes_total` | Counter | `target_group`, `outcome` |

along with the standard Go runtime and process metrics. `path` is the route
(`/api/v1/metrics/taps/:id/selections`), not the path of the request, and
`unmatched` for a request which matches no route. The duration of the two
streaming routes is not observed. `outcome` is `success` or the code of a
`scrape_error`, and a scrape shared by several taps counts once.

The endpoint is protected the way the Antrea components protect theirs:

- The caller authenticates with a Kubernetes bearer token, typically the
  ServiceAccount token of the Prometheus server. This goes through the
  `Authorization: Bearer` fallback of the API, so `auth.bearerToken.enable`
  must be left on (it is by default). With it off, a Prometheus server has no
  way to authenticate.
- The caller must hold `get` on the `/metrics` non-resource URL, which Antrea
  UI checks with a `SelfSubjectAccessReview` made with the caller's token, on
  every scrape. The ClusterRole from the Antrea documentation for Prometheus
  works unchanged.

A scrape job, for a Prometheus server running in the cluster with a
ServiceAccount bound to such a ClusterRole:

```yaml
scrape_configs:
  - job_name: antrea-ui
    # https if https.enable is set, with the matching tls_config.
    scheme: http
    authorization:
      credentials_file: /var/run/secrets/kubernetes.io/serviceaccount/token
    static_configs:
      - targets: ["antrea-ui.kube-system.svc:3000"]
```

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: prometheus-antrea
rules:
  - nonResourceURLs: ["/metrics"]
    verbs: ["get"]
```

Note that `/metrics` on the Service port belongs to the backend: the web UI
cannot use it as a client-side route.

## Configuration

| Helm value | Default | Description |
| --- | --- | --- |
| `metrics.enabled` | `true` | Enables the tap API and `GET /metrics`, and creates the scraper ServiceAccount and the RBAC described above |
| `metrics.minScrapeInterval` | `5s` | Shortest interval a tap can ask for, between `1s` and `5m` |
| `metrics.maxTaps` | `50` | Open taps, in total |
| `metrics.maxTargetsPerTap` | `20` | Targets per tap |
| `metrics.maxRequestsPerSecond` | `2` | Requests per second and per user to `GET /targets`, `GET /families` and `POST /taps`. A negative value disables the limit, for test environments |

With `metrics.enabled` set to `false`, the tap routes answer 501, `GET
/metrics` answers 404, and none of the ServiceAccount, the token and Node
permissions and the connections to Node addresses exist. The backend still
instruments itself, but nothing can read the result. The `metrics.ui.antrea.io`
rule stays in `antrea-ui-admin-core` either way, where it does nothing, so that
the rule list of that role does not depend on values.
