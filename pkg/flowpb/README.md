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

The current copies are from antrea-io/antrea@35f47b8 (antrea-io/antrea#8432).
They carry antrea-io/antrea#8276, which added `GetFlowsRequest.cluster_wide`,
`GetFlowsRequest.namespaces`, `FlowKubernetes.source_disclosure`,
`FlowKubernetes.destination_disclosure` and the `EndpointDisclosure` enum, and
antrea-io/antrea#8432, which added `GetFlowsRequest.resume`,
`GetFlowsResponse.resume_token` and the `ResumeToken` message.
