{{- define "frontendImageTag" -}}
{{- if .Values.frontend.image.tag }}
{{- .Values.frontend.image.tag -}}
{{- else if eq .Chart.AppVersion "latest" }}
{{- print "latest" -}}
{{- else }}
{{- print "v" .Chart.AppVersion -}}
{{- end }}
{{- end -}}

{{- define "frontendImage" -}}
{{- print .Values.frontend.image.repository ":" (include "frontendImageTag" .) -}}
{{- end -}}

{{- define "backendImageTag" -}}
{{- if .Values.backend.image.tag }}
{{- .Values.backend.image.tag -}}
{{- else if eq .Chart.AppVersion "latest" }}
{{- print "latest" -}}
{{- else }}
{{- print "v" .Chart.AppVersion -}}
{{- end }}
{{- end -}}

{{- define "backendImage" -}}
{{- print .Values.backend.image.repository ":" (include "backendImageTag" .) -}}
{{- end -}}

{{- define "cookieSecure" -}}
{{- if eq (toString .Values.security.cookieSecure) "true" }}
{{- true -}}
{{- else if eq (toString .Values.security.cookieSecure) "false" }}
{{- false -}}
{{- else }}
{{- .Values.https.enable -}}
{{- end }}
{{- end -}}

{{- define "oidcProviderName" -}}
{{- .Values.auth.oidc.providerName -}}
{{- end -}}

{{- define "oidcIssuerURL" -}}
{{- .Values.auth.oidc.issuerURL -}}
{{- end -}}

{{- /* An optional discovery URL, distinct from the issuer URL, for deployments where the issuer is
a public / external address that the backend cannot reach (or cannot reach yet: the external address
may not resolve until the antrea-ui Pod is Ready, and the Pod is not Ready until OIDC discovery has
succeeded). Empty means "discover at the issuer URL", which is the normal case. */ -}}
{{- define "oidcDiscoveryURL" -}}
{{- .Values.auth.oidc.discoveryURL -}}
{{- end -}}

{{- define "oidcClientID" -}}
{{- .Values.auth.oidc.clientID -}}
{{- end -}}

{{- define "oidcClientSecret" -}}
{{- .Values.auth.oidc.clientSecret -}}
{{- end -}}

{{- define "oidcClientIDSecretName" -}}
{{- if not (empty .Values.auth.oidc.clientIDSecretRef.name) -}}
{{- .Values.auth.oidc.clientIDSecretRef.name -}}
{{- else -}}
antrea-ui-oidc-client
{{- end -}}
{{- end -}}

{{- define "oidcClientSecretSecretName" -}}
{{- if not (empty .Values.auth.oidc.clientSecretSecretRef.name) -}}
{{- .Values.auth.oidc.clientSecretSecretRef.name -}}
{{- else -}}
antrea-ui-oidc-client
{{- end -}}
{{- end -}}

{{- define "oidcClientIDKey" -}}
{{- if or (empty .Values.auth.oidc.clientIDSecretRef.name) (empty .Values.auth.oidc.clientIDSecretRef.key) -}}
clientID
{{- else -}}
{{- .Values.auth.oidc.clientIDSecretRef.key -}}
{{- end -}}
{{- end -}}

{{- define "oidcClientSecretKey" -}}
{{- if or (empty .Values.auth.oidc.clientSecretSecretRef.name) (empty .Values.auth.oidc.clientSecretSecretRef.key) -}}
clientSecret
{{- else -}}
{{- .Values.auth.oidc.clientSecretSecretRef.key -}}
{{- end -}}
{{- end -}}

{{- /* -------------------------------- */}}

{{- define "validateValues" -}}

{{- if .Values.https.enable -}}
{{- if not ( has .Values.https.method ( list "auto" "user" "userCA" "secret" ) ) -}}
{{- fail "https.method is not valid" -}}
{{- end -}}
{{- end -}}

{{- /* The four modes are independent, and none of them is privileged over the others: a deployment
that only lets users bring their own kubeconfig or paste a token is a supported (and more locked
down) configuration than one with the shared admin password. Keep this list in step with
AuthConfig.anyModeEnabled in pkg/config/server/config.go, which the backend enforces on startup. */ -}}
{{- if not ( or .Values.auth.basic.enable .Values.auth.oidc.enable .Values.auth.kubeconfig.enable .Values.auth.token.enable ) -}}
{{- fail "at least one authentication method must be enabled (auth.basic, auth.oidc, auth.kubeconfig, auth.token)" -}}
{{- end -}}

{{- /* Built-in Dex was removed: it ran as a sidecar on localhost, which the kube-apiserver has no
route to and no reason to trust as an issuer. Now that the id_token is the credential Antrea UI
presents upstream, a provider the apiserver does not trust cannot work at all. Fail loudly rather
than silently ignoring leftover values, which would look like a working OIDC deployment right up
until the first Kubernetes call. */ -}}
{{- if .Values.dex -}}
{{- fail "built-in Dex support has been removed; use auth.oidc.* to point Antrea UI at an OIDC provider that the kube-apiserver also trusts (see docs/oidc.md), and delete the dex.* values" -}}
{{- end -}}

{{- if and .Values.auth.oidc.enable ( empty .Values.url ) -}}
{{- fail "url is required when OIDC is enabled" -}}
{{- end -}}

{{- if .Values.auth.oidc.enable -}}
{{- if empty .Values.auth.oidc.issuerURL -}}
{{- fail "auth.oidc.issuerURL is required when OIDC is enabled" -}}
{{- end -}}
{{- if and (empty .Values.auth.oidc.clientID) (empty .Values.auth.oidc.clientIDSecretRef.name) -}}
{{- fail "either auth.oidc.clientID or auth.oidc.clientIDSecretRef.name is required when OIDC is enabled" -}}
{{- end -}}
{{- if and (not (empty .Values.auth.oidc.clientID)) (not (empty .Values.auth.oidc.clientIDSecretRef.name)) -}}
{{- fail "auth.oidc.clientID and auth.oidc.clientIDSecretRef.name are mutually exclusive" -}}
{{- end -}}
{{- if and (empty .Values.auth.oidc.clientSecret) (empty .Values.auth.oidc.clientSecretSecretRef.name) -}}
{{- fail "either auth.oidc.clientSecret or auth.oidc.clientSecretSecretRef.name is required when OIDC is enabled" -}}
{{- end -}}
{{- if and (not (empty .Values.auth.oidc.clientSecret)) (not (empty .Values.auth.oidc.clientSecretSecretRef.name)) -}}
{{- fail "auth.oidc.clientSecret and auth.oidc.clientSecretSecretRef.name are mutually exclusive" -}}
{{- end -}}
{{- end -}}

{{- end -}}
