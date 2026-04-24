{{- define "antrea-ui.nginx.conf" }}
{{- $port := .Values.frontend.port -}}
server {
    {{- if .Values.https.enable }}
    listen       {{ $port }} ssl;
    {{- if .Values.ipv6.enable }}
    listen       [::]:{{ $port }} ssl;
    {{- end }}
    {{- else }}
    listen       {{ $port }};
    {{- if .Values.ipv6.enable }}
    listen       [::]:{{ $port }};
    {{- end }}
    {{- end }}
    server_name _;
    root /app;
    index index.html;
    client_max_body_size 10M;

    {{- if .Values.https.enable }}
    ssl_certificate /app/ssl/tls.crt;
    ssl_certificate_key /app/ssl/tls.key;
    {{- end }}

    # Use top-level locations only. Nested locations under "location /" are easy to misconfigure and
    # can prevent /api and /auth from reaching the backend (browser sees axios "Network Error").
    location /api/ {
        proxy_http_version 1.1;
        proxy_pass_request_headers on;
        proxy_hide_header Access-Control-Allow-Origin;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_pass http://127.0.0.1:{{ .Values.backend.port }};
        {{- $secure := include "cookieSecure" . -}}
        {{- if eq $secure "true" }}
        proxy_cookie_flags ~ httponly secure;
        {{- else }}
        proxy_cookie_flags ~ httponly;
        {{- end }}
    }

    location /auth/ {
        proxy_http_version 1.1;
        proxy_pass_request_headers on;
        proxy_hide_header Access-Control-Allow-Origin;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_pass http://127.0.0.1:{{ .Values.backend.port }};
        {{- $secure := include "cookieSecure" . -}}
        {{- if eq $secure "true" }}
        proxy_cookie_flags ~ httponly secure;
        {{- else }}
        proxy_cookie_flags ~ httponly;
        {{- end }}
    }

    {{- if .Values.dex.enable }}
    location /dex {
        proxy_http_version 1.1;
        proxy_pass_request_headers on;
        proxy_hide_header Access-Control-Allow-Origin;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_pass http://127.0.0.1:5556;
    }
    {{- end }}

    location / {
        try_files $uri $uri/ /index.html;
    }
}
{{- end }}
