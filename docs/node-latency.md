# Node Latency Dashboard

The **Node Latency** dashboard visualizes the inter-Node round-trip latency
measurements reported by Antrea's
[NodeLatencyMonitor](https://antrea.io/docs/main/docs/features/node-latency-monitor/)
feature. It reads the cluster-scoped `NodeLatencyStats` resources from the
`stats.antrea.io/v1alpha1` API (the same data returned by
`kubectl get nodelatencystats`).

## Prerequisites

The `NodeLatencyMonitor` feature gate must be enabled in the Antrea Agent, and a
`NodeLatencyMonitor` resource must be configured to start measurements. If no
measurements are available, the dashboard shows an empty state with these
instructions. No additional `antrea-ui` configuration is required; the data is
fetched through the existing Kubernetes API proxy.

## Views

- **Summary cards**: aggregate statistics over all measured links — node count,
  measured links, down links, and mean / median / P90 / max latency (ms).
- **Heatmap** (default): a ping-mesh matrix with source Nodes on the Y axis and
  target Nodes on the X axis, colored from green (low RTT) to red (high RTT).
  Down links are gray. Problem-node axis labels are colored red.
- **Table**: a flat list of every measurement (source, target, target IP, RTT,
  last send / receive times).
- **Search**: select a Node by name to open a detail panel listing all of its
  egress links (this Node → peers) and ingress links (peers → this Node), each
  with status, RTT, target IPs, and last receive time.
- **Problem Nodes**: lists Nodes with multiple down ingress or egress links.

## How a link is classified as "down"

A measurement (one target IP of one peer) is considered **down** when either:

- it has no measured RTT (`lastMeasuredRTTNanoseconds` is absent or zero), or
- its `lastRecvTime` lags the reference time by more than 3 minutes
  (`DOWN_STALENESS_MS`), i.e. no probe reply has been received recently.

The reference time is the newest `lastSendTime` observed across the whole cluster
rather than the browser clock, to avoid false positives from clock skew between
the browser and the cluster. A source→target link is down only when all of its
target IPs are down. A Node is flagged as a problem when it has 2 or more down
ingress or egress links (`PROBLEM_DOWN_THRESHOLD`).

## Large clusters

Better handling possible? Need reviews. 