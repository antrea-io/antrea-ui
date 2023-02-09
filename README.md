# Antrea UI

Antrea UI is a web user interface for the Antrea CNI. It provides runtime
information about Antrea components and provides a graphical interface to run
Traceflow requests. In the future, additional functionality will be added.

## Getting Started

To install Antrea UI in your K8s cluster, you will need to install [Helm
3](https://helm.sh/docs/intro/install/).

If you have not done it already, add the Antrea Helm chart repository:

```bash
helm repo add antrea https://charts.antrea.io
helm repo update
```

Finally, install the Antrea UI chart in the same namespace as the Antrea CNI:

```bash
helm install antrea-ui antrea/antrea-ui --namespace kube-system
```

The command will display some useful information about how to access the UI. If
you installed the chart using the command above, you will need to:

1. Forward a local port to the `antrea-ui` Service: `kubectl -n kube-system port-forward service/antrea-ui 3000:3000`
2. Connect to this local port with your browser, by visiting `http://localhost:3000`

The default `admin` password is `admin`.

For more installation options, refer to the [Getting
Started](docs/getting-started.md) document.
