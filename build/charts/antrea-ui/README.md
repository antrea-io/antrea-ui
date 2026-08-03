# antrea-ui

![Version: 0.8.0-dev](https://img.shields.io/badge/Version-0.8.0--dev-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: latest](https://img.shields.io/badge/AppVersion-latest-informational?style=flat-square)

Web UI for the Antrea Kubernetes network plugin

**Homepage:** <https://antrea.io/>

## Source Code

* <https://github.com/antrea-io/antrea-ui>

## Requirements

Kubernetes: `>= 1.28.0-0`

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Affinity for the Antrea UI Pod. |
| antreaNamespace | string | `"kube-system"` | Namespace where Antrea is installed. |
| auth.basic.enable | bool | `true` | Enable password-based authentication (the static "admin" password). Kubernetes API calls made by these sessions are impersonated as the antrea-ui-admin ServiceAccount, so every user logging in this way has exactly the same cluster access. |
| auth.bearerToken.enable | bool | `true` | Enable the "Authorization: Bearer <token>" fallback on the Antrea UI API, for non-browser clients (a script, a controller, the e2e suite) that authenticate every request with a Kubernetes token instead of holding a session cookie. Such a request creates no session, and its token is validated against the API server on the request itself, since there is no login step to validate it at. The browser UI never sends this header, so turning it off costs the UI nothing; leave it on only if something other than a browser talks to the API. |
| auth.kubeconfig.enable | bool | `false` | Enable "bring your own kubeconfig" authentication: the user uploads a kubeconfig and Antrea UI uses the current context's credential (a token, or a client certificate and key) on their behalf. Credentials that would have to run a program on the user's machine (exec plugins, auth-provider) are rejected. |
| auth.oidc.clientID | string | `""` | Application (client) ID to be used by the Antrea UI server to identify itself to the OIDC provider. |
| auth.oidc.clientIDSecretRef.key | string | `"clientID"` | Name of the key field storing the application (client) ID in the referenced secret. |
| auth.oidc.clientIDSecretRef.name | string | `""` | Name of the secret containing the application (client) ID to be used by the Antrea UI server to identify itself to the OIDC provider. The secret must exist in the Namespace of the Helm release. It is mutually exclusive with the clientID value. |
| auth.oidc.clientSecret | string | `""` | Application secret to be used by the Antrea UI server to identify itself to the OIDC provider. Note that this secret will never be exposed to the UI frontend and to users. It should be base64-encoded. |
| auth.oidc.clientSecretSecretRef.key | string | `"clientSecret"` | Name of the key field storing the application secret in the referenced secret. |
| auth.oidc.clientSecretSecretRef.name | string | `""` | Name of the secret containing the application secret to be used by the Antrea UI server to identify itself to the OIDC provider. The secret must exist in the Namespace of the Helm release. It is mutually exclusive with the clientSecret value. |
| auth.oidc.discoveryURL | string | `""` | Address at which to perform OIDC discovery, when it differs from issuerURL. Only needed when the issuer is a public address the backend Pod cannot reach (or cannot reach until it is itself Ready, which would deadlock startup). Leave empty to discover at issuerURL, which is the normal case. |
| auth.oidc.enable | bool | `false` | Enable OIDC-based authentication: the server connects to an OIDC provider to authenticate users. When enabling OIDC authentication, you will need to set the top-level url value. The kube-apiserver must be configured to trust the *same* issuer, via the --oidc-issuer-url and --oidc-client-id flags, because the id_token is what Antrea UI presents to it on the user's behalf. See docs/oidc.md. |
| auth.oidc.issuerURL | string | `""` | URL of the OIDC provider. The server will use the URL to retrieve the OpenID Provider Configuration Document, which should be available at the /.well-known/openid-configuration endpoint. The kube-apiserver must be configured to trust this same issuer, since the id_token it issues is the credential Antrea UI presents to Kubernetes on the user's behalf. |
| auth.oidc.logoutURL | string | `""` | URL to log out of the OIDC provider. It will be invoked when the user logs out of the Antrea UI. Some OIDC providers may not offer this capability. If this is empty, the user will stay signed into the identity provider even after logging out of the Antrea UI. The provided URL will be processed by a template engine, and the following template values are supported: {{Token}} (the ID token issued by the provider), {{ClientID}} (the application ID), {{URL}} (the URL at which Antrea UI is accessible), and {{LogoutReturnURL}} (useful if you want to redirect back to Antrea UI after signing out from the identity provider, with a helpful user-facing message). |
| auth.oidc.providerName | string | `""` | Name of the OIDC provider (Dex, Github OAuth2, ...). This is used for user-facing messages, and does not have any impact on functionality. |
| auth.oidc.scopes | list | `["openid","email","groups","offline_access"]` | Scopes to request from the OIDC provider. "offline_access" is what makes the provider issue a refresh token, without which a session ends as soon as the id_token does (often only minutes). "groups" populates the group claims that group-based Kubernetes RBAC needs. "email" is requested because --oidc-username-claim=email is by far the most common kube-apiserver configuration, and providers omit the claim if the scope was not asked for. |
| auth.serviceAccountToken.enable | bool | `true` | Enable Kubernetes token authentication on the login page: the user pastes a bearer token (typically a ServiceAccount token) and Antrea UI creates a session from it, like any other login mode. Independent of auth.bearerToken below, which accepts the same credential as a per-request API header rather than as a login. |
| backend.extraVolumeMounts | list | `[]` | Additional volumeMounts. |
| backend.image | object | `{"pullPolicy":"IfNotPresent","repository":"antrea/antrea-ui-backend","tag":""}` | Container image to use for the Antrea UI backend. |
| backend.logVerbosity | int | `0` | Log verbosity switch for backend server. |
| backend.port | int | `8080` | Container port on which the backend will listen. |
| backend.resources | object | `{}` | Resource requests and limits for the backend container. |
| extraVolumes | list | `[]` | Additional volumes. |
| flowAggregator.address | string | `"flow-aggregator.flow-aggregator.svc:14740"` | gRPC address (host:port) of the FlowStreamService. |
| flowAggregator.caConfigMap | string | `"flow-aggregator-ca"` | Name of the ConfigMap (in namespace below) containing the CA certificate (key: ca.crt) used to verify the FlowStreamService server certificate. The FlowStreamService uses server-side TLS only (no client authentication). Leave empty to skip server certificate verification (dev/test only). |
| flowAggregator.enabled | bool | `false` | When true, the backend connects to Flow Aggregator's FlowStreamService over gRPC. |
| flowAggregator.insecureSkipVerify | bool | `false` | Disable TLS server certificate verification. Should only be used for development or testing; never enable this in production. |
| flowAggregator.namespace | string | `"flow-aggregator"` | Namespace where the Flow Aggregator is installed. |
| flowAggregator.serverName | string | `""` | Override the TLS server name used for certificate verification. Useful when dialing via kubectl port-forward (loopback address) while the server cert is issued for the in-cluster Service DNS name (e.g. flow-aggregator.flow-aggregator.svc). Leave empty to use the hostname from the address field. |
| frontend.extraVolumeMounts | list | `[]` | Additional volumeMounts. |
| frontend.image | object | `{"pullPolicy":"IfNotPresent","repository":"antrea/antrea-ui-frontend","tag":""}` | Container image to use for the Antrea UI frontend. |
| frontend.port | int | `3000` | Container port on which the frontend will listen. |
| frontend.resources | object | `{}` | Resource requests and limits for the frontend container. |
| hostAliases | list | `[]` | Additional entries for the Pod's /etc/hosts, in the standard PodSpec hostAliases form. Useful when the backend has to reach a name that cluster DNS cannot resolve - an OIDC issuer served outside the cluster, for example - without having to touch CoreDNS. Each entry is `{ip: <address>, hostnames: [<name>, ...]}`. |
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
| plugins.labelSelector | string | `"ui.antrea.io/plugin=true"` | Label selector for the ConfigMaps (in the namespace below) that the backend watches for frontend plugins. |
| plugins.namespace | string | `""` | Namespace to watch for plugin ConfigMaps. Defaults to the release namespace. Set this to isolate plugin ConfigMaps away from antrea-ui's own release namespace - useful since antrea-ui is commonly installed into kube-system, which can host other sensitive ConfigMaps. If set to anything other than the release namespace, whoever runs `helm install`/`upgrade` needs permission to create a Role/RoleBinding in that other namespace too. |
| podAnnotations | object | `{}` | Annotations to be added to the Antrea UI Pod. |
| podLabels | object | `{}` | Labels to be added to the Antrea UI Pod. |
| security.cookieSecure | bool | same as https.enable | Set the Secure attribute for Antrea UI cookies. The attribute is set by default when HTTPS is enabled in Antrea UI (by setting https.enable to true). When using an Ingress to terminate TLS, you should explicitly set cookieSecure to true for security hardening purposes. |
| service.annotations | object | `{}` | Annotations to be added to the Service. |
| service.externalTrafficPolicy | string | `nil` | Override the ExternalTrafficPolicy for the Service. Set it to Local to route Service traffic to Node-local endpoints only. |
| service.labels | object | `{}` | Labels to be added to the Service. |
| service.nodePort | int | `31234` | - The Node port to use when the Service type is NodePort or LoadBalancer. |
| service.port | int | `3000` | The port on which the Service is exposed. |
| service.type | string | `"ClusterIP"` | - The type of Service used for Antrea UI access, either ClusterIP, NodePort or LoadBalancer. |
| session.idleTimeout | string | `"30m"` | How long a session survives with no request. The UI pings the backend every 5 minutes while a tab is visible, so this is really "how long with no open visible tab". |
| session.maxLifetime | string | `"12h"` | Absolute cap on a session's lifetime, however active the user is. |
| session.maxSessions | int | `1000` | Maximum number of concurrent sessions the backend will hold. |
| session.maxSessionsPerUser | int | `10` | Maximum number of concurrent sessions one identity may hold. This is what keeps a single user from filling maxSessions and denying logins to everyone else. Logging in past the cap evicts that user's own least-recently-used session rather than failing the login. Must be <= maxSessions. Admin-password sessions are exempt: they all authenticate as the same "admin", so capping them would give every user of that password one shared budget. |
| tolerations | object | `{}` | Tolerations for the Antrea UI Pod. |
| url | string | `""` | Address at which the Antrea UI is accessible. Not required for most configurations. |

----------------------------------------------
Autogenerated from chart metadata using [helm-docs v1.7.0](https://github.com/norwoodj/helm-docs/releases/v1.7.0)
