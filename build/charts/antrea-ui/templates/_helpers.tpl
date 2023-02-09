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

{{- define "validateValues" -}}
{{- if .Values.https.enable -}}
{{- if not ( has .Values.https.method ( list "auto" "user" "userCA" ) ) -}}
{{- fail "https.method is not valid" -}}
{{- end -}}
{{- end -}}
{{- end -}}
