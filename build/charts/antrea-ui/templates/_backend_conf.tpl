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
  token:
    enabled: {{ .Values.auth.token.enable }}
  bearerToken:
    enabled: {{ .Values.auth.bearerToken.enable }}
  cookieSecure: {{ include "cookieSecure" . }}
session:
  idleTimeout: {{ .Values.session.idleTimeout | quote }}
  maxLifetime: {{ .Values.session.maxLifetime | quote }}
  maxSessions: {{ .Values.session.maxSessions }}
  maxSessionsPerUser: {{ .Values.session.maxSessionsPerUser }}
logVerbosity: {{ .Values.backend.logVerbosity }}
log:
  directory: "/var/log/antrea-ui"
  maxSizeMB: {{ .Values.backend.logs.maxSizeMB }}
  maxBackups: {{ .Values.backend.logs.maxBackups }}
limits:
  maxSupportBundlesPerHour: {{ .Values.supportBundle.maxBundlesPerHour }}
supportBundle:
  enabled: {{ .Values.supportBundle.enabled }}
  directory: "/var/run/antrea-ui/supportbundles"
  maxBundles: {{ .Values.supportBundle.maxBundles }}
  maxConcurrent: {{ .Values.supportBundle.maxConcurrent }}
  maxTotalBytes: {{ int64 .Values.supportBundle.maxTotalBytes }}
  maxSourceBytes: {{ int64 .Values.supportBundle.maxSourceBytes }}
  ttl: {{ .Values.supportBundle.ttl | quote }}
  collectionTimeout: {{ .Values.supportBundle.collectionTimeout | quote }}
  {{- with .Values.supportBundle.extraSources }}
  extraSources:
    {{- toYaml . | nindent 4 }}
  {{- end }}
plugins:
  labelSelector: {{ .Values.plugins.labelSelector | quote }}
  namespace: {{ .Values.plugins.namespace | quote }}
  directory: {{ .Values.plugins.directory | quote }}
  maxConfigMapPlugins: {{ .Values.plugins.maxConfigMapPlugins }}
  maxDirectoryPlugins: {{ .Values.plugins.maxDirectoryPlugins }}
  maxBundleBytes: {{ .Values.plugins.maxBundleBytes }}
{{- if .Values.plugins.signature.enabled }}
{{- include "antrea-ui.validatePluginTrustedKeys" . }}
  signature:
    trustedKeys:
    {{- range .Values.plugins.signature.trustedKeys }}
      - name: {{ .name | quote }}
        type: {{ .type | quote }}
        file: {{ printf "/app/plugin-keys/%s/%s" .name .configMap.key | quote }}
    {{- end }}
{{- end }}
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
