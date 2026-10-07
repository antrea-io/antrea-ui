# Copyright 2026 Antrea Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""A stand-in for a plugin provider's aggregated API server, for the plugin e2e tests.

It is registered with an APIService (see ci/e2e-plugins.sh), so kube-apiserver proxies requests
for /apis/ui.e2e.antrea.io/v1 to it, over TLS on port 8443. It answers only what the aggregation
layer and the backend ask for:

  GET /apis/ui.e2e.antrea.io/v1                            the group's discovery document, which
                                                           kube-apiserver needs for the APIService
                                                           to become Available
  GET /apis/ui.e2e.antrea.io/v1/<resource>/<sha>/download  the bundle, whatever <sha> is: the
                                                           backend verifies the digest itself

The same bundle is also served over plain HTTP on port 8080, for a plugin whose manifest selects
the http transport:

  GET /bundles/<sha>                                       the bundle, whatever <sha> is

Everything else is a 404.
"""

import http.server
import json
import ssl
import threading

GROUP_VERSION = "ui.e2e.antrea.io/v1"
PREFIX = "/apis/" + GROUP_VERSION

with open("/bundle/bundle.zip", "rb") as f:
    BUNDLE = f.read()


class Handler(http.server.BaseHTTPRequestHandler):
    def _send(self, status, content_type, body):
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        path = self.path.split("?")[0]
        parts = path.strip("/").split("/")
        if path.rstrip("/") == PREFIX:
            body = json.dumps({
                "kind": "APIResourceList",
                "apiVersion": "v1",
                "groupVersion": GROUP_VERSION,
                "resources": [],
            }).encode()
            self._send(200, "application/json", body)
        elif (path.startswith(PREFIX + "/") and len(parts) == 6 and parts[5] == "download") or (
                len(parts) == 2 and parts[0] == "bundles"):
            self._send(200, "application/zip", BUNDLE)
        else:
            self._send(404, "application/json", b'{"kind":"Status","apiVersion":"v1","status":"Failure","code":404}')


ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
ctx.load_cert_chain("/tls/tls.crt", "/tls/tls.key")
tls_server = http.server.ThreadingHTTPServer(("", 8443), Handler)
tls_server.socket = ctx.wrap_socket(tls_server.socket, server_side=True)
threading.Thread(target=tls_server.serve_forever, daemon=True).start()
http.server.ThreadingHTTPServer(("", 8080), Handler).serve_forever()
