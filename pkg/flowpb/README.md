# pkg/flowpb

The three `.pb.go` files are generated here from the `.proto` files of the upstream
Antrea repository, [antrea-io/antrea](https://github.com/antrea-io/antrea),
`pkg/apis/flow/v1alpha1/`. This repository has no `.proto` source of its own: the
revision of Antrea the files were generated from is recorded in `UPSTREAM_SHA`.

| File in this directory | Generated from                         |
| ---------------------- | -------------------------------------- |
| `flow.pb.go`           | `pkg/apis/flow/v1alpha1/flow.proto`    |
| `service.pb.go`        | `pkg/apis/flow/v1alpha1/service.proto` |
| `service_grpc.pb.go`   | `pkg/apis/flow/v1alpha1/service.proto` |

`policy.go` is written by hand and is not touched by regeneration.

The protos are used as upstream has them, except for `go_package`, which is
rewritten to `antrea.io/antrea-ui/pkg/flowpb`. The proto package name is
unchanged, so the gRPC full method name stays wire-identical to the Flow
Aggregator's. As a result, the generated files differ from upstream's own in the
`go_package` bytes of the embedded descriptor and, possibly, in the formatting of
doc comments.

To move to another upstream revision, run [`hack/update-flowpb.sh`](../../hack/update-flowpb.sh)
with the revision, which must be a full commit SHA unless `ANTREA_DIR` points to a
local Antrea checkout:

```sh
hack/update-flowpb.sh <antrea-commit-sha>
```

It regenerates the three files and rewrites `UPSTREAM_SHA` only if every step
succeeds. Without an argument it regenerates from the revision in `UPSTREAM_SHA`.
The script requires the `protoc` and plugin versions that Antrea uses; its header
lists them and says where to get them. Review the diff of the generated files
together with the upstream proto changes between the old and the new `UPSTREAM_SHA`.

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
