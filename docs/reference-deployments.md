# Reference Deployments for Antrea UI

This document is meant to cover some common situations when deploying Antrea UI
in a K8s cluster. It is not meant to be comprehensive, and you will need to
adjust configuration parameters to suit your specific use case.

- [LoadBalancer Service with MetalLB + cert-manager (self-signed)](#loadbalancer-service-with-metallb--cert-manager-self-signed)

## LoadBalancer Service with MetalLB + cert-manager (self-signed)

### Prerequisites

* You will need a K8s cluster with Antrea as the CNI.
* We assume that you want to use your own PKI, with a self-signed CA
  certificate. The CA will be used to sign the certificate for Antrea UI. You
  will need the PEM data for the CA private key and the CA certificate. To
  easily access the Antrea UI from your browser, you will also need to add your
  CA certificate to the trust store of your operating system.
* MetalLB is [installed](https://metallb.universe.tf/installation/).
* cert-manager is [installed](https://cert-manager.io/docs/installation/).

### Configure MetalLB

The configuration will depend on your deployment. Please refer to the MetalLB
[documentation](https://metallb.universe.tf/configuration/). You will need an
`IPAddressPool`, from which an IP address can be assigned to the Antrea UI
LoadBalancer Service. If using MetalLB in L2 mode, the resources you create may
look like this:

```yaml
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: example
  namespace: metallb-system
---
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: first-pool
  namespace: metallb-system
spec:
  addresses:
  - 192.168.77.60-192.168.77.70
```

In the example above, the `IPAddressPool` constitues of IP range 192.168.77.60 -
192.168.77.70, from which we will assign an address to Antrea UI. This IP
address range is reserved from the Node subnet.

### Configure cert-manager

Since we are bringing our own self-signed CA, we will be using the cert-manager
[CA issuer](https://cert-manager.io/docs/configuration/ca/). The following
resources will need to be created:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: my-ca
  namespace: kube-system
data:
  tls.crt: <base64-encoded PEM data for CA certificate>
  tls.key: <base64-encoded PEM data for CA private key>
---
apiVersion: cert-manager.io/v1
kind: Issuer
metadata:
  name: my-ca-issuer
  namespace: kube-system
spec:
  ca:
    secretName: my-ca
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: antrea-ui
  namespace: kube-system
spec:
  commonName: antrea-ui
  dnsNames:
  - antrea-ui.local
  secretName: antrea-ui-tls
  privateKey:
    algorithm: ECDSA
    size: 256
  issuerRef:
    name: my-ca-issuer
    kind: Issuer
    group: cert-manager.io
```

We are creating all resources in the kube-system Namespace, as this is where we
will install Antrea UI.

### Install Antrea UI with Helm

```bash
cat <<EOF >> values.yml
https:
  enable: true
  method: "secret"
service:
  annotations:
    metallb.universe.tf/address-pool: first-pool
  type: LoadBalancer
  externalTrafficPolicy: Cluster
  port: 443
EOF

helm install antrea-ui antrea/antrea-ui --namespace kube-system -f values.yml
```

Make sure you provide a valid `IPAddressPool` name in the
`metallb.universe.tf/address-pool` annotation.

### Accessing the UI

The certificate issued by cert-manager is valid for subject name
"antrea-ui.local". To avoid getting a browser privacy error when accessing
Antrea UI, we need to use that name instead of the IP address allocated to the
Service by MetalLB. The easiest way to achieve this is to edit your local
`/etc/hosts` file with a new entry:

```bash
ANTREA_UI_IP=$(kubectl -n kube-system get svc/antrea-ui -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
echo "$ANTREA_UI_IP antrea-ui.local" | sudo tee -a /etc/hosts
```

The IP address allocated by MetalLB is "sticky", so you do not need to worry
about the address changing, e.g., because of restarts.

Alternatively, you can assign a static IP address to the LoadBalancer Service
using the `metallb.universe.tf/loadBalancerIPs`
[annotation](https://metallb.universe.tf/usage/#requesting-specific-ips), and
include the IP address as a subject name in the `Certificate` request (using the
`spec.ipAddresses` field). You will not have a "user-friendly" name to access
the UI, but you will not need to configure a DNS entry.

After that, you will be able to access the UI by visiting
`https://antrea-ui.local`, without any error or warning (assuming that your
previously added your self-signed CA certificate to the trust store of your
operating system).
