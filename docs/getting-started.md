# Getting Started

## Prerequisites

* You will need a Kubernetes cluster running Antrea. Antrea UI v1.0.0
  requires Antrea (and the Flow Aggregator, if flow visibility is enabled) at
  v2.8 or later.
* Ensure that Helm 3 is [installed](https://helm.sh/docs/intro/install/). We
  recommend using a recent version of Helm if possible. Refer to the [Helm
  documentation](https://helm.sh/docs/topics/version_skew/) for compatibility
  between Helm and Kubernetes versions.
* Add the Antrea Helm chart repository:

  ```bash
  helm repo add antrea https://charts.antrea.io
  helm repo update
  ```

## Installation

To install the Antrea UI Helm chart, use the following command:

```bash
helm install antrea-ui antrea/antrea-ui --namespace kube-system
```

The chart should be installed in the same namespace as Antrea.

This will install the latest released version of Antrea UI. You can also install
a specific version of the UI with `--version <TAG>`.

To install an unreleased version of Antrea UI, you will need to clone this
repository:

```bash
git clone https://github.com/antrea-io/antrea-ui.git
cd antrea-ui
helm install antrea-ui build/charts/antrea-ui --namespace kube-system
```

## Upgrade

To upgrade the Antrea UI Helm chart, use the following commands:

```bash
helm upgrade antrea-ui antrea/antrea-ui --namespace kube-system [--version <TAG>]
```

## Chart Values

See the [chart values reference](../build/charts/antrea-ui/README.md) for all
supported settings.

## Flow visibility

Enable `flowAggregator.enabled` to view live flows from an existing Flow
Aggregator (FA) with FlowStreamService enabled. By default, the UI connects to
`flow-aggregator.flow-aggregator.svc:14740`; configure `flowAggregator.address`
and `flowAggregator.namespace` for a different deployment. Setting
`flowAggregator.enabled=false` disables all flow visibility, including history.

The `caConfigMap` setting names a ConfigMap containing the public CA in
`ca.crt`. Static connections read it in the configured `namespace`; leaving
`caConfigMap` empty uses the system trust store. TLS checks the certificate and
server name; use `serverName` to override the name when connecting through a
port-forward. See the [chart values](../build/charts/antrea-ui/README.md) for
all options and [flow permissions](authentication.md#flow-data-is-per-user)
for user access requirements.

### Historical flows

Deploy a compatible historical query service and its storage separately, then
configure the UI client through Helm values:

```yaml
flowAggregator:
  enabled: true
flowHistory:
  enabled: true
  address: history.example.test:14740
  namespace: flow-history
  caConfigMap: history-ca
```

History is disabled by default and requires an explicit address. The UI chart
does not deploy the query service or database. With `caConfigMap` set, create
that ConfigMap in the specified namespace; the chart grants the UI access to
it. Without a CA ConfigMap, no history namespace is needed. The service must
satisfy the [historical query contract](../pkg/flowpb/README.md#historical-query-contract).

Select a historical range in Flow List or Service Map to query past flows.
Change the range or click Refresh to update the result. A failed historical
query reports an error and never substitutes live FA data.

Results contain at most 10,000 snapshots, and multiple snapshots may describe
one connection. They are not full-period aggregates: selecting seven days does
not guarantee seven days of retained data or that every match fits within the
limit. Aggregation granularity and an exact truncation indicator are not
supported; live throughput is not calculated for historical results.

## Accessing the web UI

If you installed the Helm chart using the command above, without overriding any
chart values, you will need to:

1. Forward a local port to the `antrea-ui` Service: `kubectl -n kube-system port-forward service/antrea-ui 3000:3000`
2. Connect to this local port with your browser, by visiting `http://localhost:3000`

The default `admin` password is `admin`. You can change it in the `Settings`
tab.

### Using HTTPS

We recommend accessing the Antrea UI over HTTPS. It can be done one of 2 ways:

1. If you are using an [Ingress Controller](https://kubernetes.io/docs/concepts/services-networking/ingress-controllers/),
   you can terminate TLS at Ingress. This does not require any customization
   when installing the antrea-ui Helm chart. You need to create the appropriate
   [Ingress resource](https://kubernetes.io/docs/concepts/services-networking/ingress/),
   exposing an HTTPS route for the antrea-ui Service. Refer to [this](reference-deployments.md#ingress-with-nginx--cert-manager-lets-encrypt-on-eks)
   for an example.
2. Otherwise, you can enable HTTPS directly in the Antrea UI web server. There
   are different 4 methods for doing so, which are described below.

#### `auto`: Helm generates a self-signed certificate

This is the simplest option to enable HTTPS. Helm will generate a self-signed
TLS certificate and key, which will be used by the web server. Note that the
certificate will be re-generated every time the Helm template function runs.

```bash
cat <<EOF >> values-auto.yml
https:
  enable: true
  method: "auto"
EOF

helm install antrea-ui antrea/antrea-ui --namespace kube-system -f values-auto.yml
```

When the installation completes, the certificate PEM data will be displayed, in
case you would like to import it into the trust store for your browser.

#### `user`: User provides a certificate

With this option, you will need to provide your own TLS certificate and
key. This is useful if you already have your own trusted CA certificate that you
can use to generate a new signed certificate.

```bash
cat <<EOF >> values-user.yml
https:
  enable: true
  method: "user"
  user:
    cert: "<base64-encoded PEM certificate>"
    key: "<base64-encoded PEM key>"
EOF

helm install antrea-ui antrea/antrea-ui --namespace kube-system -f values-user.yml
```

The certificate should include `localhost` as a Subject Alternate Name (SAN).

#### `userCA`: User provides a CA certificate

With this option, you will need to provide your own CA certificate and
key. Helm will generate a signed certificate using the provided data.

```bash
cat <<EOF >> values-userCA.yml
https:
  enable: true
  method: "userCA"
  userCA:
    cert: "<base64-encoded PEM CA certificate>"
    key: "<base64-encoded PEM CA key>"
EOF

helm install antrea-ui antrea/antrea-ui --namespace kube-system -f values-userCA.yml
```

#### `secret`: User provides a Secret of type kubernetes.io/tls

With this option, you will need to provide a K8s Secret of type
kubernetes.io/tls, which includes the tls.crt and tls.key data fields.

```bash
cat <<EOF >> values-secret.yml
https:
  enable: true
  method: "secret"
  secretName: "<name>"
EOF

helm install antrea-ui antrea/antrea-ui --namespace kube-system -f values-secret.yml
```

If you do not provide a `secretName`, it will default to `"antrea-ui-tls"`.

With this method, you can integrate with [cert-manager](https://cert-manager.io/).
Refer to [this](reference-deployments.md#loadbalancer-service-with-metallb--cert-manager-self-signed)
for an example.

### Using Single Sign-On (SSO) with OIDC

Refer to this [document](oidc.md) for instructions on how to configure SSO for
Antrea UI.

### Logging in as your own Kubernetes identity

Besides the admin password, Antrea UI can act as the logged-in user's *own*
Kubernetes identity — via OIDC, an uploaded kubeconfig, or a pasted Kubernetes
token — so that what each user can see and do is decided by their own RBAC.
Refer to this [document](authentication.md) for the available login modes, the
RBAC an administrator has to grant, and how sessions expire.
