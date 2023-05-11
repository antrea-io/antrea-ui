# Reference Deployments for Antrea UI

This document is meant to cover some common situations when deploying Antrea UI
in a K8s cluster. It is not meant to be comprehensive, and you will need to
adjust configuration parameters to suit your specific use case.

- [LoadBalancer Service with MetalLB + cert-manager (self-signed)](#loadbalancer-service-with-metallb--cert-manager-self-signed)
- [Ingress with Nginx + cert-manager (Let's Encrypt) on EKS](#ingress-with-nginx--cert-manager-lets-encrypt-on-eks)

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

## Ingress with Nginx + cert-manager (Let's Encrypt) on EKS

### Prerequisites

For this example, we will use an EKS cluster, running Antrea in
`networkPolicyOnly` mode. Refer to the Antrea
[documentation](https://github.com/antrea-io/antrea/blob/main/docs/eks-installation.md#deploying-antrea-in-networkpolicyonly-mode)
for more information on this. Note that this is just one example deployment, and
that the rest of this section is widely applicable to many K8s clusters, whether
they use cloud-managed K8s services or not.

Our K8s cluster is also running the following software:

* [NGINX Ingress Controller](https://docs.nginx.com/nginx-ingress-controller/),
  to expose an HTTP route to the Antrea UI Service.
* [AWS Load Balancer Controller](https://docs.aws.amazon.com/eks/latest/userguide/aws-load-balancer-controller.html),
  to expose the NGINX Ingress Controller as a LoadBalancer Service and make it
  publicly accessible.
* [cert-manager](https://cert-manager.io/), to automatically generate TLS
  certificates for our Ingress routes.
* [ExternalDNS](https://github.com/kubernetes-sigs/external-dns), to
  automatically publish DNS records for our Ingress routes. In our case, we will
  use a domain name registered with AWS Route 53, and ExternalDNS has been
  configured to update the corresponding Hosted Zone.

The NGINX Ingress Controller, cert-manager and ExternalDNS are not specific to
EKS / AWS, and are used in many production clusters. Other providers offer
alternatives to the AWS Load Balancer Controller, and bare-metal clusters can
use [MetalLB](https://metallb.universe.tf/).

The purpose of this document is not to provide comprehensive installation and
configuration instructions for the above software. Instead, we want to focus on
the necessary steps for Antrea UI installation.

However, for the sake of reference, the NGINX Ingress Controller was installed
with the following values:

```yaml
controller:
  service:
    type: LoadBalancer
    annotations:
      service.beta.kubernetes.io/aws-load-balancer-name: apps-ingress
      service.beta.kubernetes.io/aws-load-balancer-type: external
      service.beta.kubernetes.io/aws-load-balancer-scheme: internet-facing
      service.beta.kubernetes.io/aws-load-balancer-nlb-target-type: ip
      service.beta.kubernetes.io/aws-load-balancer-healthcheck-protocol: http
      service.beta.kubernetes.io/aws-load-balancer-healthcheck-path: /healthz
      service.beta.kubernetes.io/aws-load-balancer-healthcheck-port: 10254
```

To deploy ExternalDNS on AWS, refer to the following
[document](https://github.com/kubernetes-sigs/external-dns/blob/master/docs/tutorials/aws.md).
Make sure that you use the correct domain filter, and that ExternalDNS is
configured to watch Ingress resources.

### Configure cert-manager

This is the issuer we create to issue our TLS certificates through Let's
Encrypt. Our example domain name (`abas.link`) is managed through AWS Route 53,
hence our DNS solver configuration. In our case, we use static AWS credentials,
but there are better ways to configure access for EKS clusters. Refer to the
cert-manager [documentation](https://cert-manager.io/docs/configuration/acme/dns01/route53/).

```yaml
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-acme
spec:
  acme:
    email: <YOUR EMAIL ADDRESS>
    # The ACME server URL
    server: https://acme-v02.api.letsencrypt.org/directory
    # Name of a secret used to store the ACME account private key
    privateKeySecretRef:
      name: letsencrypt-private-key
    solvers:
    - dns01:
        route53:
          region: <AWS_REGION>
          accessKeyID: <AWS_ACCESS_KEY_ID>
          secretAccessKeySecretRef:
            name: <secret name>
            key: <secret key for AWS_SECRET_ACCESS_KEY>
      # Optional, use if you need to filter by domain name
      # selector:
      #   dnsNames:
      #   - 'abas.link'
      #   - '*.abas.link'
```

### Install Antrea UI with Helm

You do not need any customization when installing the Antrea UI with Helm. Just
run:

```bash
helm install antrea-ui antrea/antrea-ui --namespace kube-system
```

### Create the Ingress Resource

We create the following Ingress resource to expose an HTTPS route to the Antrea
UI:

```bash
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  namespace: kube-system
  name: antrea-ui
  annotations:
    kubernetes.io/ingress.class: "nginx"
    # this should match the name of the ClusterIssuer resource defined above
    cert-manager.io/cluster-issuer: "letsencrypt-acme"
spec:
  tls:
  - hosts:
    # use the correct domain name for you
    - antrea-ui.abas.link
    secretName: antrea-ui-tls
  rules:
  # use the correct domain name for you
  - host: antrea-ui.abas.link
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: antrea-ui
            port:
              # port used by default by the antrea-ui frontend
              number: 3000
```

For more details and configuration options, you can refer to the cert-manager
[documentation](https://cert-manager.io/docs/tutorials/acme/nginx-ingress/).

### Accessing the UI

You should now be able to access the UI using the hostname specified in the
Ingress resource. In our case, we can access the UI by visiting
`https://antrea-ui.abas.link/`.
