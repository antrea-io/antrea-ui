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

Flow visibility reads from sources implementing `FlowStreamService`, such as
the Flow Aggregator (FA). Deploy sources separately; the UI chart configures
only the client connections.

### Flow sources

`flowSources` is the complete configuration. Each entry needs a unique `name`,
an `address` and `capabilities`: `watch` (live streams), `list` (finite queries),
or both. Entries have no defaults; for example:

```yaml
flowAggregator:
  enabled: false
flowSources:
  - name: flow-aggregator
    capabilities: [watch]
    address: flow-aggregator.flow-aggregator.svc:14740
    namespace: flow-aggregator
    caConfigMap: flow-aggregator-ca
```

`flowAggregator.enabled=true` remains a supported Helm shorthand. It inserts
FA as the first `watch` source, before `flowSources`.

With `caConfigMap` set, create that ConfigMap in the source's `namespace`
with the CA under `ca.crt`. The chart grants `get`, `list` and `watch` on that
ConfigMap. Each source watches its CA independently: a missing or invalid initial
bundle leaves only that source unavailable, while the backend keeps running.
After a valid bundle is loaded, a deleted ConfigMap or an invalid replacement
keeps the last good bundle. A CA change replaces that source's shared connection:
live clients reconnect, while an interrupted finite query fails and must be re-run.
Client-certificate sessions pick up the new CA on their next request.
With no CA ConfigMap, the backend uses the system trust store. With
`insecureSkipVerify`, it does not watch the ConfigMap. Keep TLS verification enabled in production:
`insecureSkipVerify: true` trusts any server at the configured address with every
viewer's credentials.

This UI uses only the first `watch` source and ignores `list` sources.
Without a `watch` source, the Flows page reports that visibility is unavailable.
Finite queries are for other UIs built on the shared components.

See the [chart values](../build/charts/antrea-ui/README.md) for all options and
[flow sources](authentication.md#flow-sources) for permissions and request
routing. Sources must satisfy the
[flow source contract](../pkg/flowpb/README.md#flow-source-contract).

Hand-written backend configurations must replace `flowAggregator` with
`flowSources`; the old key is rejected at startup. Existing Helm values remain
supported.

### Flow source discovery

A cluster may run multiple Flow Aggregators, only some of which expose
FlowStreamService. To discover the Service instead of configuring its address,
enable discovery on the Flow Aggregator:

```yaml
flowAggregator:
  enabled: true
  discovery:
    enabled: true
  caConfigMap: flow-aggregator-ca
```

A `flowSources` entry takes the same setting, `discovery: {enabled: true}`, and
at most one source can enable it, since the label selects a single endpoint.

Discovery is disabled by default. When enabled, the UI watches Services across
all namespaces for `observability.antrea.io/flow-stream-service=true` and
selects the `flowstream-grpc` TCP port. Antrea's FA chart adds the label when
FlowStreamService is enabled, which is the default in Aggregate mode, from
Antrea v2.8.0. An older Flow Aggregator does not label its Service, so
discovery finds nothing for it: use a static address there. The UI ignores the
static address and namespace and reads the named CA ConfigMap in the selected
Service's namespace.

`discovery.labelSelector` replaces the default label selector, for a Service
labeled differently:

```yaml
flowAggregator:
  discovery:
    enabled: true
    labelSelector: app=flow-aggregator,tier=visibility
```

It takes the syntax of `kubectl -l` and requires `discovery.enabled`. Keep it as
narrow as the default: anyone who can create a matching Service in any
namespace receives the credentials the UI forwards.

With no match, or several, the source is unavailable and flow visibility shows
an error until there is exactly one; the rest of the UI remains available. The
error does not name the Services, since every user who opens a flow stream sees
it: the backend logs the candidates. Use a static address if several Services
match. Other flow sources keep their static
configuration.

The backend follows the Service. When its address, its namespace, its
`flowstream-grpc` port or target port, its cluster IP, its selector or its
identity (it was recreated) changes, it reconnects:
live streams end and their clients reconnect, including those of sessions logged
in with a client certificate, and a finite query in flight fails and must be
re-run. When the namespace changes, the source stays unavailable until the CA
ConfigMap in the new namespace is loaded. Updates of the Service that leave
these as they were, such as an annotation, do nothing. A CA rotation behaves as
described for [flow sources](#flow-sources).

The chart grants cluster-wide Service list and watch, and get, list and watch
on ConfigMaps with the configured CA name, only when a source discovers. Matching
Services and their CA ConfigMaps must be controlled by trusted administrators
because the UI forwards user credentials to the endpoint; see the
[trust requirements](authentication.md#flow-service-discovery-trust).

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
