# Copyright 2026 Antrea Authors.
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

# Helpers shared by the ci/e2e-*.sh scripts. It is meant to be sourced, not run:
#
#   source "${THIS_DIR}/e2e-lib.sh"

# e2e_generate_ca_and_cert DIR CA_CN HOSTNAME generates a throwaway CA named CA_CN, and a server
# certificate for HOSTNAME signed by it, as DIR/ca.crt, DIR/tls.crt and DIR/tls.key. DIR must
# exist.
function e2e_generate_ca_and_cert() {
    local dir="$1" ca_cn="$2" hostname="$3"
    # An openssl.cnf is used rather than -addext so that this works with the LibreSSL that ships
    # with macOS, which does not support -addext.
    cat > "${dir}/openssl.cnf" <<EOF
[req]
distinguished_name = dn
[dn]
[v3_ca]
basicConstraints = critical,CA:TRUE
keyUsage = critical,keyCertSign,cRLSign
[v3_server]
basicConstraints = critical,CA:FALSE
keyUsage = critical,digitalSignature,keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = DNS:${hostname}
EOF

    openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
        -keyout "${dir}/ca.key" -out "${dir}/ca.crt" \
        -subj "/CN=${ca_cn}" \
        -config "${dir}/openssl.cnf" -extensions v3_ca 2>/dev/null

    openssl req -newkey rsa:2048 -nodes \
        -keyout "${dir}/tls.key" -out "${dir}/tls.csr" \
        -subj "/CN=${hostname}" \
        -config "${dir}/openssl.cnf" 2>/dev/null

    openssl x509 -req -in "${dir}/tls.csr" -days 3650 \
        -CA "${dir}/ca.crt" -CAkey "${dir}/ca.key" -CAcreateserial \
        -out "${dir}/tls.crt" \
        -extfile "${dir}/openssl.cnf" -extensions v3_server 2>/dev/null

    rm -f "${dir}/tls.csr"
}

# e2e_wait_for_apiservice NAMESPACE DEPLOYMENT APISERVICE waits for DEPLOYMENT, the aggregated API
# server behind APISERVICE, to be rolled out, then for kube-apiserver to report APISERVICE as
# Available, i.e. reachable through the aggregation layer.
function e2e_wait_for_apiservice() {
    local namespace="$1" deployment="$2" apiservice="$3"
    kubectl rollout status --namespace "${namespace}" "deployment/${deployment}" --timeout=5m
    kubectl wait --for=condition=Available "apiservice/${apiservice}" --timeout=3m
}
