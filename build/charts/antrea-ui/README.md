# antrea-ui

![Version: 0.1.0-dev](https://img.shields.io/badge/Version-0.1.0--dev-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: latest](https://img.shields.io/badge/AppVersion-latest-informational?style=flat-square)

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
| backend | object | `{"image":{"pullPolicy":"IfNotPresent","repository":"antrea/antrea-ui-backend","tag":""},"port":8080,"resources":{}}` | Configuration for the Antrea UI backend container. |
| backend.image | object | `{"pullPolicy":"IfNotPresent","repository":"antrea/antrea-ui-backend","tag":""}` | Container image to use for the Antrea UI backend. |
| backend.port | int | `8080` | Container port on which the backend will listen/ |
| backend.resources | object | `{}` | Resource requests and limits for the backend container. |
| frontend | object | `{"image":{"pullPolicy":"IfNotPresent","repository":"antrea/antrea-ui-frontend","tag":""},"port":3000,"resources":{}}` | Configuration for the Antrea UI frontend container. |
| frontend.image | object | `{"pullPolicy":"IfNotPresent","repository":"antrea/antrea-ui-frontend","tag":""}` | Container image to use for the Antrea UI frontend. |
| frontend.port | int | `3000` | Container port on which the frontend will listen. |
| frontend.resources | object | `{}` | Resource requests and limits for the frontend container. |
| https | object | `{"auto":{"commonName":"localhost","daysValid":365,"dnsNames":[],"ipAddresses":[]},"enable":false,"method":"auto","user":{"cert":"","key":""},"userCA":{"cert":"","commonName":"localhost","daysValid":365,"dnsNames":[],"ipAddresses":[],"key":""}}` | HTTPS configuration for the Antrea UI. |
| https.auto | object | `{"commonName":"localhost","daysValid":365,"dnsNames":[],"ipAddresses":[]}` | Configure automatic TLS certificate generation with Helm. |
| https.auto.commonName | string | `"localhost"` | Common name to use in the certificate. |
| https.auto.daysValid | int | `365` | Number of days for which the certificate will be valid. There is no automatic rotation with this method. |
| https.auto.dnsNames | list | `[]` | DNS names to use in the certificate. |
| https.auto.ipAddresses | list | `[]` | IP addresses to use in the certificate. |
| https.enable | bool | `false` | Enable HTTPS (only) for accessing the web UI. |
| https.method | string | `"auto"` | Method for generating the TLS certificate for the web server. We support "auto", "user", and "userCA". With "auto", Helm will generate a new self-signed certificate every time the template function is executed. With "user", the user is responsible for providing a certificate and key, which will be used directly. With "userCA", the user is responsible for providing a CA certificate and key, which will be used to generate a signed certificate to be used by the web server. |
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
| ipv6 | object | `{"enable":true}` | IPv6 configuration for the Antrea UI. |
| ipv6.enable | bool | `true` | Enable IPv6 for accessing the web UI. Even if the cluster does not support IPv6, you do not typically need to set this value to false. |
| nodeSelector | object | `{"kubernetes.io/os":"linux"}` | Node selector for the Antrea UI Pod. |
| podAnnotations | object | `{}` | Annotations to be added to the Antrea UI Pod. |
| podLabels | object | `{}` | Labels to be added to the Antrea UI Pod. |
| service | object | `{"annotations":{},"labels":{},"nodePort":31234,"port":3000,"type":"ClusterIP"}` | Configuration for the Antrea UI Service. |
| service.annotations | object | `{}` | Annotations to be added to the Service. |
| service.labels | object | `{}` | Labels to be added to the Service. |
| service.nodePort | int | `31234` | - The Node port to use when the Service type is NodePort or LoadBalancer. |
| service.port | int | `3000` | The port on which the Service is exposed. |
| service.type | string | `"ClusterIP"` | - The type of Service used for Antrea UI access, either ClusterIP, NodePort or LoadBalancer. |
| tolerations | object | `{}` | Tolerations for the Antrea UI Pod. |

----------------------------------------------
Autogenerated from chart metadata using [helm-docs v1.7.0](https://github.com/norwoodj/helm-docs/releases/v1.7.0)
