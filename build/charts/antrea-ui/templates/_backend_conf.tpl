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
    scopes:
      {{- toYaml .Values.auth.oidc.scopes | nindent 6 }}
  kubeconfig:
    enabled: {{ .Values.auth.kubeconfig.enable }}
  serviceAccountToken:
    enabled: {{ .Values.auth.serviceAccountToken.enable }}
  bearerToken:
    enabled: {{ .Values.auth.bearerToken.enable }}
  cookieSecure: {{ include "cookieSecure" . }}
session:
  idleTimeout: {{ .Values.session.idleTimeout | quote }}
  maxLifetime: {{ .Values.session.maxLifetime | quote }}
  maxSessions: {{ .Values.session.maxSessions }}
  maxSessionsPerUser: {{ .Values.session.maxSessionsPerUser }}
logVerbosity: {{ .Values.backend.logVerbosity }}
plugins:
  labelSelector: {{ .Values.plugins.labelSelector | quote }}
  namespace: {{ .Values.plugins.namespace | default .Release.Namespace | quote }}
  directory: {{ .Values.plugins.directory | quote }}
  maxConfigMapPlugins: {{ .Values.plugins.maxConfigMapPlugins }}
  maxDirectoryPlugins: {{ .Values.plugins.maxDirectoryPlugins }}
  maxConfigMapBundleBytes: {{ .Values.plugins.maxConfigMapBundleBytes }}
flowAggregator:
  enabled: {{ .Values.flowAggregator.enabled }}
  address: {{ .Values.flowAggregator.address | quote }}
{{- if .Values.flowAggregator.enabled }}
  caConfigMap: {{ .Values.flowAggregator.caConfigMap | quote }}
  namespace: {{ .Values.flowAggregator.namespace | default "flow-aggregator" | quote }}
  serverName: {{ .Values.flowAggregator.serverName | quote }}
  insecureSkipVerify: {{ .Values.flowAggregator.insecureSkipVerify }}
{{- end }}
{{- end }}
