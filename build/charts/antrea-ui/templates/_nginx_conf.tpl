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

    location / {
        # $host strips the port from the Host header; $http_host preserves it. The backend
        # compares this against a redirect target's host (e.g. /auth/logout's redirect_url) to
        # decide whether it stays on antrea-ui's own origin, so a stripped port makes every such
        # comparison fail whenever antrea-ui is served on a non-default port.
        proxy_set_header Host $http_host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Real-IP $remote_addr;

        # Flow SSE can be idle for a long time when the Flow Aggregator ring buffer has no matching
        # records; nginx's default proxy_read_timeout (~60s) then returns 504 to the browser.
        location /api/v1/flows/stream {
            proxy_http_version 1.1;
            proxy_pass_request_headers on;
            proxy_hide_header Access-Control-Allow-Origin;
            proxy_set_header Connection '';
            proxy_buffering off;
            proxy_read_timeout 86400s;
            proxy_send_timeout 86400s;
            proxy_pass http://127.0.0.1:{{ .Values.backend.port }};
            {{- $secure := include "cookieSecure" . -}}
            {{- if eq $secure "true" }}
            proxy_cookie_flags ~ httponly secure;
            {{- else }}
            proxy_cookie_flags ~ httponly;
            {{- end }}
        }

        location /api {
            proxy_http_version 1.1;
            proxy_pass_request_headers on;
            proxy_hide_header Access-Control-Allow-Origin;
            proxy_pass http://127.0.0.1:{{ .Values.backend.port }};
            # ensure the correct flags are set, even though the api server should already be setting them
            {{- $secure := include "cookieSecure" . -}}
            {{- if eq $secure "true" }}
            proxy_cookie_flags ~ httponly secure;
            {{- else }}
            proxy_cookie_flags ~ httponly;
            {{- end }}
        }

        # at the moment, the config is the same as for /api
        location /auth {
            proxy_http_version 1.1;
            proxy_pass_request_headers on;
            proxy_hide_header Access-Control-Allow-Origin;
            proxy_pass http://127.0.0.1:{{ .Values.backend.port }};
            # ensure the correct flags are set, even though the api server should already be setting them
            {{- $secure := include "cookieSecure" . -}}
            {{- if eq $secure "true" }}
            proxy_cookie_flags ~ httponly secure;
            {{- else }}
            proxy_cookie_flags ~ httponly;
            {{- end }}
        }

        location / {
            try_files $uri $uri/ /index.html;
        }
    }
}
{{- end }}
