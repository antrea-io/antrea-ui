{{- define "antrea-ui.backend.conf" }}
addr: ":{{ .Values.backend.port }}"
auth:
  basic:
    jwtKeyPath: "/app/jwt-key.pem"
  cookieSecure: {{ include "cookieSecure" . }}
{{- end }}
