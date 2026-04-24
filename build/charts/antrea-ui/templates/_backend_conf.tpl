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
{{- if and .Values.flowAggregator.enabled .Values.flowAggregator.mtls.enabled }}
  caCert: "/etc/flow-aggregator-mtls/ca.crt"
  certFile: "/etc/flow-aggregator-mtls/tls.crt"
  keyFile: "/etc/flow-aggregator-mtls/tls.key"
{{- else }}
  caCert: {{ .Values.flowAggregator.caCert | quote }}
  certFile: {{ .Values.flowAggregator.certFile | quote }}
  keyFile: {{ .Values.flowAggregator.keyFile | quote }}
{{- end }}
{{- end }}
