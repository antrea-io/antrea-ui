{{- define "antrea-ui.backend.conf" }}
addr: ":{{ .Values.backend.port }}"
url: {{ .Values.url | quote }}
antreaNamespace: {{ .Values.antreaNamespace | quote }}
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
logVerbosity: {{ .Values.backend.logVerbosity }}
flowAggregator:
  enabled: {{ .Values.flowAggregator.enabled }}
  address: {{ .Values.flowAggregator.address | quote }}
{{- if .Values.flowAggregator.enabled }}
  caConfigMap: {{ .Values.flowAggregator.caConfigMap | quote }}
  clientSecret: {{ .Values.flowAggregator.clientSecret | quote }}
  namespace: {{ .Values.flowAggregator.namespace | default "flow-aggregator" | quote }}
{{- end }}
{{- end }}
