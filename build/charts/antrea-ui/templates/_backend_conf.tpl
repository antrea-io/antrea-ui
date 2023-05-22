{{- define "antrea-ui.backend.conf" }}
addr: ":{{ .Values.backend.port }}"
url: {{ .Values.url }}
auth:
  basic:
    enabled: {{ .Values.auth.basic.enable }}
  oidc:
    enabled: {{ .Values.auth.oidc.enable }}
    issuerURL: {{ include "oidcIssuerURL" . }}
    discoveryURL: {{ include "oidcDiscoveryURL" . }}
    providerName: {{ include "oidcProviderName" . }}
    logoutURL: {{ .Values.auth.oidc.logoutURL | quote }}
  jwtKeyPath: "/app/jwt-key.pem"
  cookieSecure: {{ include "cookieSecure" . }}
logVerbosity: {{ .Values.logVerbosity }}
{{- end }}
