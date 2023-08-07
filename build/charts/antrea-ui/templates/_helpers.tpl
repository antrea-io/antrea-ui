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
{{- ternary "Dex" .Values.auth.oidc.providerName .Values.dex.enable -}}
{{- end -}}

{{- define "oidcIssuerURL" -}}
{{- ternary (print .Values.url "/dex") .Values.auth.oidc.issuerURL .Values.dex.enable -}}
{{- end -}}

{{- /* When using Dex we need to use a dicovery URL which is different from the issuer URL, in case
the issuer URL is a public / external address. This is to avoid some cyclic dependency: the external
address may not be accessible until the antrea-ui Pod is Ready, and the antrea-ui Pod will not be
ready until OIDC discovery is successful. */ -}}
{{- define "oidcDiscoveryURL" -}}
{{- ternary "http://localhost:5556/dex" "" .Values.dex.enable -}}
{{- end -}}

{{- define "oidcClientID" -}}
{{- ternary (print "antrea-ui") .Values.auth.oidc.clientID .Values.dex.enable -}}
{{- end -}}

{{- define "oidcClientSecret" -}}
{{- ternary (randAlphaNum 64 | b64enc) .Values.auth.oidc.clientSecret .Values.dex.enable -}}
{{- end -}}

{{- /* -------------------------------- */}}

{{- define "validateValues" -}}

{{- if .Values.https.enable -}}
{{- if not ( has .Values.https.method ( list "auto" "user" "userCA" "secret" ) ) -}}
{{- fail "https.method is not valid" -}}
{{- end -}}
{{- end -}}

{{- if and ( not .Values.auth.basic.enable ) ( not .Values.auth.oidc.enable) -}}
{{- fail "at least one authentication method must be enabled (Basic / OIDC)" -}}
{{- end -}}

{{- if and .Values.dex.enable ( not .Values.auth.oidc.enable ) -}}
{{- fail "cannot enable built-in Dex without enabling OIDC auth" -}}
{{- end -}}

{{- if and .Values.auth.oidc.enable ( empty .Values.url ) -}}
{{- fail "url is required when OIDC is enabled" -}}
{{- end -}}

{{- if and .Values.auth.oidc.enable (not .Values.dex.enable) -}}
{{- if empty .Values.auth.oidc.issuerURL -}}
{{- fail "auth.oidc.issuerURL is required when OIDC is enabled" -}}
{{- end -}}
{{- if empty .Values.auth.oidc.clientID -}}
{{- fail "auth.oidc.clientID is required when OIDC is enabled" -}}
{{- end -}}
{{- if empty .Values.auth.oidc.clientSecret -}}
{{- fail "auth.oidc.clientSecret is required when OIDC is enabled" -}}
{{- end -}}
{{- end -}}

{{- end -}}
