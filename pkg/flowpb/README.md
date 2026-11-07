# pkg/flowpb

These files are **copied by hand** from the upstream Antrea repository, not
generated here. There is no `.proto` source in this repository and no
generation target in `Makefile` or `hack/`.

Source: [antrea-io/antrea](https://github.com/antrea-io/antrea),
`pkg/apis/flow/v1alpha1/`:

| File in this directory | Upstream file                             |
| ---------------------- | ----------------------------------------- |
| `flow.pb.go`           | `pkg/apis/flow/v1alpha1/flow.pb.go`         |
| `service.pb.go`        | `pkg/apis/flow/v1alpha1/service.pb.go`      |
| `service_grpc.pb.go`   | `pkg/apis/flow/v1alpha1/service_grpc.pb.go` |

The only local edit is the package clause: upstream declares `package
v1alpha1`, and these copies declare `package flowpb`. The Apache license
header at the top of each file is upstream's own and is kept as is.

To refresh them after an upstream proto change, re-copy all three together and
re-apply the package rename, for example:

```sh
for f in flow.pb.go service.pb.go service_grpc.pb.go; do
    git -C /path/to/antrea show main:pkg/apis/flow/v1alpha1/$f \
        | sed 's/^package v1alpha1$/package flowpb/' > pkg/flowpb/$f
done
```

The current copies are from antrea-io/antrea@eb44118 (antrea-io/antrea#8564).
They carry antrea-io/antrea#8276, which added `GetFlowsRequest.cluster_wide`,
`GetFlowsRequest.namespaces`, `FlowKubernetes.source_disclosure`,
`FlowKubernetes.destination_disclosure` and the `EndpointDisclosure` enum,
antrea-io/antrea#8432, which added `GetFlowsRequest.resume`,
`GetFlowsResponse.resume_token` and the `ResumeToken` message, and
antrea-io/antrea#8471, which changed when an endpoint is disclosed in full (the
client holds the stream's own verb on `flows` in the endpoint's Namespace) and
so only the `EndpointDisclosure` comments, and antrea-io/antrea#8491, which limits the
Namespace withheld at the Flow tier to the destination of a denied connection the client
initiated, again only in the `EndpointDisclosure` comments. antrea-io/antrea#8563 changed
how a `FlowFilter` is matched, and so only the `FlowFilter` comments: its directional criteria
describe a single endpoint, and `service_names` accepts `namespace/name`.
antrea-io/antrea#8569 changed when a stream that does not follow closes, and so only the
`GetFlowsRequest.follow` comment. antrea-io/antrea#8564 added
`FlowKubernetes.destination_service_disclosure`: the destination Service is disclosed at the tier
of its own Namespace, and `destination_disclosure` no longer covers it.

## Flow source contract

A flow source is any service the UI backend is configured to read flows from
(`flowSources`). It implements `FlowStreamService.GetFlows` as
upstream `service.proto` describes. A source declares which capabilities
it serves: `watch` for `follow=true` and `list` for `follow=false`, which the
UI sends with `since`, `max_count`, scope and peer filters. A compatible source
must:

- Authenticate the forwarded user credential, authorize the verb for the
  requested mode (`watch` or `list` on `flows.observability.antrea.io`) in the
  requested scope, and apply the `EndpointDisclosure` rules.
- Send a non-empty `stream_epoch`, stable for the server process, in the first
  response of every stream, even when no record matches.
- Honor `max_count` and cancellation, and finish a finite query with a
  successful EOF right after the last record. The backend caps a query at
  1,000,000 records and has no resume logic. It asks the source for one record
  more than the limit: receiving that record proves the result was truncated.
  `max_count` follows the order in which the source examines records, as
  `service.proto` says, so a source cannot be assumed to return the newest
  records, and which records make it into a truncated result is arbitrary; any
  such promise belongs to the UI using that source.
- A source may cap `max_count`, but must reject a larger value (for example
  with `InvalidArgument`) rather than end the stream early. `service.proto`
  ends a finite stream only when every record is accounted for or `max_count`
  is reached, and an early end would make a truncated result look complete.
  The backend asks for up to 1,000,001 records, so a source with a lower cap
  must reject such a request, not cut it short.
- Return errors for failed or interrupted queries. `service.proto` gives no
  meaning to `Unavailable` or `FailedPrecondition`; this backend classifies
  them as retryable and reports them as `flow_source_unavailable`. It never
  falls back to another source.

The backend sends an SSE completion event only after the successful completion
of a finite query; EOF at the browser without that event means the query was
interrupted. That event carries `truncated`: true when the source delivered the
record beyond the limit, which the backend drops. A source that sends more than
one record beyond the limit is failed. The current protocol has no aggregation
granularity. See [Flow sources](../../docs/getting-started.md#flow-sources)
for configuration.
