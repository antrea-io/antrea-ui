# antrea-ui

![Version: 0.6.0](https://img.shields.io/badge/Version-0.6.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: latest](https://img.shields.io/badge/AppVersion-latest-informational?style=flat-square)

Web UI for the Antrea Kubernetes network plugin

**Homepage:** <https://antrea.io/>

## Source Code

* <https://github.com/antrea-io/antrea-ui>

## Requirements

Kubernetes: `>= 1.16.0-0`

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Affinity for the Antrea UI Pod. |
| auth.basic.enable | bool | `true` | Enable password-based authentication. |
| auth.oidc.clientID | string | `""` | Application (client) ID to be used by the Antrea UI server to identify itself to the OIDC provider. |
| auth.oidc.clientSecret | string | `""` | Application (secret) ID to be used by the Antrea UI server to identify itself to the OIDC provider. Note that this secret will never be exposed to the UI frontend and to users. It should be base64-encoded. |
| auth.oidc.enable | bool | `false` | Enable OIDC-based authentication: the server connects to an OIDC provider to authenticate users. When enabling OIDC authentication, you will need to set the top-level url value. |
| auth.oidc.issuerURL | string | `""` | URL of the OIDC provider. The server will use the URL to retrieve the OpenID Provider Configuration Document, which should be available at the /.well-known/openid-configuration endpoint. |
| auth.oidc.logoutURL | string | `""` | URL to log out of the OIDC provider. It will be invoked when the user logs out of the Antrea UI. Some OIDC providers may not offer this capability. If this is empty, the user will stay signed into the identity provider even after logging out of the Antrea UI. The provided URL will be processed by a template engine, and the following template values are supported: {{Token}} (the ID token issued by the provider), {{ClientID}} (the application ID), {{URL}} (the URL at which Antrea UI is accessible), and {{LogoutReturnURL}} (useful if you want to redirect back to Antrea UI after signing out from the identity provider, with a helpful user-facing message). |
| auth.oidc.providerName | string | `""` | Name of the OIDC provider (Dex, Github OAuth2, ...). This is used for user-facing messages, and does not have any impact on functionality. |
| backend.image | object | `{"pullPolicy":"IfNotPresent","repository":"antrea/antrea-ui-backend","tag":""}` | Container image to use for the Antrea UI backend. |
| backend.logVerbosity | int | `0` | Log verbosity switch for backend server. |
| backend.port | int | `8080` | Container port on which the backend will listen. |
| backend.resources | object | `{}` | Resource requests and limits for the backend container. |
| dex.config.connectors | list | `[]` | Dex connectors configuration (refer to https://dexidp.io/docs/connectors/). |
| dex.enable | bool | `false` | Enable built-in Dex for OIDC authentication. |
| dex.image | object | `{"pullPolicy":"IfNotPresent","repository":"ghcr.io/dexidp/dex","tag":"v2.36.0-distroless"}` | Container image to use for Dex. |
| dex.resources | object | `{}` | Resource requests and limits for the Dex container. |
| frontend.image | object | `{"pullPolicy":"IfNotPresent","repository":"antrea/antrea-ui-frontend","tag":""}` | Container image to use for the Antrea UI frontend. |
| frontend.port | int | `3000` | Container port on which the frontend will listen. |
| frontend.resources | object | `{}` | Resource requests and limits for the frontend container. |
| https.auto | object | `{"commonName":"localhost","daysValid":365,"dnsNames":[],"ipAddresses":[]}` | Configure automatic TLS certificate generation with Helm. |
| https.auto.commonName | string | `"localhost"` | Common name to use in the certificate. |
| https.auto.daysValid | int | `365` | Number of days for which the certificate will be valid. There is no automatic rotation with this method. |
| https.auto.dnsNames | list | `[]` | DNS names to use in the certificate. |
| https.auto.ipAddresses | list | `[]` | IP addresses to use in the certificate. |
| https.enable | bool | `false` | Enable HTTPS (only) for accessing the web UI. When using an Ingress to terminate TLS, you do not need to enable HTTPS here. |
| https.method | string | `"auto"` | Method for generating the TLS certificate for the web server. We support "auto", "user", "userCA", and "secret". With "auto", Helm will generate a new self-signed certificate every time the template function is executed. With "user", the user is responsible for providing a certificate and key, which will be used directly. With "userCA", the user is responsible for providing a CA certificate and key, which will be used to generate a signed certificate to be used by the web server. With "secret", the user is responsible for providing a secret of type kubernetes.io/tls, in the Namespace of the release. The secret must include the tls.crt and tls.key data fields. |
| https.secret.secretName | string | `"antrea-ui-tls"` | Name of the secret containing the PEM data for the certificate and private key to use. Secret must be of type kubernetes.io/tls. The typical use case is a secret generated by cert-manager. The secret must exist in the Namespace of the Helm release (typically, kube-system). |
| https.user | object | `{"cert":"","key":""}` | Use the provided TLS certificate and key. |
| https.user.cert | string | `""` | Certificate (base64-encoded PEM format) |
| https.user.key | string | `""` | Private key (base64-encoded PEM format) |
| https.userCA | object | `{"cert":"","commonName":"localhost","daysValid":365,"dnsNames":[],"ipAddresses":[],"key":""}` | Use the provided CA certificate and key to generate a signed certificate. |
| https.userCA.cert | string | `""` | CA certificate (base64-encoded PEM format) |
| https.userCA.commonName | string | `"localhost"` | Common name to use in the certificate. |
| https.userCA.daysValid | int | `365` | Number of days for which the certificate will be valid. There is no automatic rotation with this method. |
| https.userCA.dnsNames | list | `[]` | DNS names to use in the certificate. |
| https.userCA.ipAddresses | list | `[]` | IP addresses to use in the certificate. |
| https.userCA.key | string | `""` | CA private key (base64-encoded PEM format) |
| ipv6.enable | bool | `true` | Enable IPv6 for accessing the web UI. Even if the cluster does not support IPv6, you do not typically need to set this value to false. |
| nodeSelector | object | `{"kubernetes.io/os":"linux"}` | Node selector for the Antrea UI Pod. |
| podAnnotations | object | `{}` | Annotations to be added to the Antrea UI Pod. |
| podLabels | object | `{}` | Labels to be added to the Antrea UI Pod. |
| security.cookieSecure | bool | same as https.enable | Set the Secure attribute for Antrea UI cookies. The attribute is set by default when HTTPS is enabled in Antrea UI (by setting https.enable to true). When using an Ingress to terminate TLS, you should explicitly set cookieSecure to true for security hardening purposes. |
| service.annotations | object | `{}` | Annotations to be added to the Service. |
| service.externalTrafficPolicy | string | `nil` | Override the ExternalTrafficPolicy for the Service. Set it to Local to route Service traffic to Node-local endpoints only. |
| service.labels | object | `{}` | Labels to be added to the Service. |
| service.nodePort | int | `31234` | - The Node port to use when the Service type is NodePort or LoadBalancer. |
| service.port | int | `3000` | The port on which the Service is exposed. |
| service.type | string | `"ClusterIP"` | - The type of Service used for Antrea UI access, either ClusterIP, NodePort or LoadBalancer. |
| tolerations | object | `{}` | Tolerations for the Antrea UI Pod. |
| url | string | `""` | Address at which the Antrea UI is accessible. Not required for most configurations. |

----------------------------------------------
Autogenerated from chart metadata using [helm-docs v1.7.0](https://github.com/norwoodj/helm-docs/releases/v1.7.0)
