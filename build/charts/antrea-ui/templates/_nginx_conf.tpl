{{- define "antrea-ui.nginx.conf" }}
{{- $port := .Values.frontend.port -}}
# $http_host is empty for HTTP/1.0 clients (and other clients) that send no Host header, and
# nginx drops a proxy_set_header whose value is empty, so the backend would see no Host at all.
# Fall back to $host (which is always set) in that case.
map $http_host $forwarded_host {
    default $http_host;
    ''      $host;
}

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
    {{- if .Values.https.http2 }}
    # Browsers only negotiate HTTP/2 over TLS (via ALPN), so it is only enabled with HTTPS.
    http2 on;
    {{- end }}
    {{- end }}

    location / {
        # $host strips the port from the Host header; $http_host preserves it. The backend
        # compares this against a redirect target's host (e.g. /auth/logout's redirect_url) to
        # decide whether it stays on antrea-ui's own origin, so a stripped port makes every such
        # comparison fail whenever antrea-ui is served on a non-default port.
        proxy_set_header Host $forwarded_host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Real-IP $remote_addr;

        # The directives below are inherited by all the proxying locations nested in this one,
        # which therefore only have to set proxy_pass.
        proxy_http_version 1.1;
        proxy_hide_header Access-Control-Allow-Origin;
        # ensure the correct flags are set, even though the api server should already be setting them
        {{- $secure := include "cookieSecure" . -}}
        {{- if eq $secure "true" }}
        proxy_cookie_flags ~ httponly secure;
        {{- else }}
        proxy_cookie_flags ~ httponly;
        {{- end }}

        # Flow SSE can be idle for a long time when the Flow Aggregator ring buffer has no matching
        # records; nginx's default proxy_read_timeout (~60s) then returns 504 to the browser.
        location /api/v1/flows/stream {
            {{- include "antrea-ui.nginx.sse" . | nindent 12 }}
        }

        # A metrics tap is an SSE stream too, and one which lasts for as long as its page is open.
        # This prefix also covers the request which updates the selection of a tap, which is an
        # ordinary request: the settings below do it no harm.
        location /api/v1/metrics/taps {
            {{- include "antrea-ui.nginx.sse" . | nindent 12 }}
        }

        # Support bundle downloads can be large: stream them instead of buffering them to nginx's
        # own temporary files.
        location /api/v1/supportbundle {
            proxy_buffering off;
            proxy_pass http://127.0.0.1:{{ .Values.backend.port }};
        }

        location /api {
            proxy_pass http://127.0.0.1:{{ .Values.backend.port }};
        }

        # at the moment, the config is the same as for /api
        location /auth {
            proxy_pass http://127.0.0.1:{{ .Values.backend.port }};
        }

        # The backend's own metrics, for a Prometheus server (see docs/metrics.md). Always
        # proxied, whether or not metrics.enabled is set: the backend then answers 404, where
        # the location below would have answered with index.html.
        location = /metrics {
            proxy_pass http://127.0.0.1:{{ .Values.backend.port }};
        }

        location / {
            try_files $uri $uri/ /index.html;
        }
    }
}
{{- end }}

{{- define "antrea-ui.nginx.sse" -}}
# proxy_set_header directives are inherited from the outer location only if this level defines
# none of its own; Connection below forces re-declaring the rest.
proxy_set_header Host $forwarded_host;
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
proxy_set_header X-Real-IP $remote_addr;
proxy_set_header Connection '';
proxy_buffering off;
proxy_read_timeout 86400s;
proxy_send_timeout 86400s;
proxy_pass http://127.0.0.1:{{ .Values.backend.port }};
{{- end }}
