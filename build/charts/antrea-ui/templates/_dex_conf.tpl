{{- define "antrea-ui.dex.conf" }}
issuer: {{ include "oidcIssuerURL" . }}

storage:
  type: memory

web:
  http: 127.0.0.1:5556

telemetry:
  http: 0.0.0.0:5558

staticClients:
  - idEnv: "ANTREA_UI_AUTH_OIDC_CLIENT_ID"
    redirectURIs:
      - "{{ .Values.url }}/auth/oauth2/callback"
    name: "Antrea UI"
    secretEnv: "ANTREA_UI_AUTH_OIDC_CLIENT_SECRET"

connectors:
  {{- toYaml .Values.dex.config.connectors | nindent 2 }}
{{- end }}
