#!/usr/bin/env bash
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

# Stands up the Dex instance that the OIDC e2e test authenticates against.
#
# Antrea UI presents the id_token to the kube-apiserver as the end user's own credential, so the
# apiserver has to trust the same issuer. That rules out running Dex inside the Pod (the apiserver
# has no route to it), which is why built-in Dex was removed. Instead Dex runs as a container on
# the same Docker network as the Kind nodes, and all three parties agree on one issuer URL:
#
#   issuer = https://dex.e2e:5556/dex
#
#   - kube-apiserver (in a Kind node container): resolves "dex.e2e" through Docker's embedded DNS,
#     thanks to the network alias below. Trusts the CA via --oidc-ca-file.
#   - antrea-ui backend (a Pod): resolves "dex.e2e" through a hostAliases entry on its own Pod,
#     built from `dex-ip` at install time. Trusts the CA via a mounted file (see
#     ci/antrea-ui-values.yml).
#   - the e2e test (a process on the host): does not resolve "dex.e2e" at all. It dials
#     127.0.0.1:5556 directly while still verifying the certificate as "dex.e2e" - see
#     dexHTTPClient in test/e2e/oidc_test.go. This is what keeps the setup working on macOS,
#     where the host cannot route to container IPs.
#
# Usage:
#   ci/e2e-oidc.sh start-dex          # run before `kind create cluster`: the CA must exist first
#   ci/e2e-oidc.sh configure-cluster  # run after the cluster is up, before installing Antrea UI
#   ci/e2e-oidc.sh dex-ip             # print Dex's address, for the chart's hostAliases value
#   ci/e2e-oidc.sh stop               # remove the Dex container

set -eo pipefail

# Kept in sync with ci/kind-config.yml, which mounts this directory into the control-plane node so
# the apiserver can read the CA. It is a fixed path rather than a mktemp one precisely so that the
# Kind config can refer to it.
DEX_DIR="/tmp/antrea-ui-e2e-dex"
DEX_HOSTNAME="dex.e2e"
DEX_PORT="5556"
DEX_CONTAINER="antrea-ui-e2e-dex"
# The network Kind puts its nodes on. Created here if it does not exist yet, since Dex has to join
# it before the cluster is created.
KIND_NETWORK="kind"
# Pinned: the mock connector's identity (kilgore@kilgore.trout, group "authors") is a contract the
# e2e test asserts on, so the Dex version should not drift underneath it.
DEX_IMAGE="ghcr.io/dexidp/dex:v2.44.0"

# Matches ci/antrea-ui-values.yml. Not a secret in any meaningful sense: it only ever exists in a
# throwaway test cluster.
OIDC_CLIENT_ID="antrea-ui"
OIDC_CLIENT_SECRET="antrea-ui-e2e-secret"
# Where the browser (the e2e test) reaches Antrea UI. test/e2e sets up port forwarding to this
# port, so it is also what Dex must redirect back to.
ANTREA_UI_URL="http://localhost:3000"

# The identity Dex's mockCallback connector always returns. With --oidc-username-claim=email the
# apiserver sees exactly this as the username, and "authors" as a group.
TEST_USER_GROUP="authors"

log() { echo "[e2e-oidc] $*"; }

generate_certs() {
    log "Generating CA and server certificate for ${DEX_HOSTNAME} in ${DEX_DIR}"
    rm -rf "${DEX_DIR}"
    mkdir -p "${DEX_DIR}"

    # An openssl.cnf is used rather than -addext so that this works with the LibreSSL that ships
    # with macOS, which does not support -addext.
    cat > "${DEX_DIR}/openssl.cnf" <<EOF
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
subjectAltName = DNS:${DEX_HOSTNAME}
EOF

    openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
        -keyout "${DEX_DIR}/ca.key" -out "${DEX_DIR}/ca.crt" \
        -subj "/CN=antrea-ui-e2e-dex-ca" \
        -config "${DEX_DIR}/openssl.cnf" -extensions v3_ca 2>/dev/null

    openssl req -newkey rsa:2048 -nodes \
        -keyout "${DEX_DIR}/tls.key" -out "${DEX_DIR}/tls.csr" \
        -subj "/CN=${DEX_HOSTNAME}" \
        -config "${DEX_DIR}/openssl.cnf" 2>/dev/null

    openssl x509 -req -in "${DEX_DIR}/tls.csr" -days 3650 \
        -CA "${DEX_DIR}/ca.crt" -CAkey "${DEX_DIR}/ca.key" -CAcreateserial \
        -out "${DEX_DIR}/tls.crt" \
        -extfile "${DEX_DIR}/openssl.cnf" -extensions v3_server 2>/dev/null

    # The apiserver runs as a non-root user in the Kind node and reads ca.crt through a hostPath
    # mount, so it has to be world-readable.
    chmod 644 "${DEX_DIR}/ca.crt" "${DEX_DIR}/tls.crt" "${DEX_DIR}/tls.key"
    rm -f "${DEX_DIR}/tls.csr"
}

write_dex_config() {
    cat > "${DEX_DIR}/config.yaml" <<EOF
issuer: https://${DEX_HOSTNAME}:${DEX_PORT}/dex

storage:
  type: memory

web:
  https: 0.0.0.0:${DEX_PORT}
  tlsCert: /etc/dex/tls.crt
  tlsKey: /etc/dex/tls.key

telemetry:
  http: 0.0.0.0:5558

oauth2:
  # There is no human to click "grant access", and the e2e test drives this with a plain HTTP
  # client that does not parse HTML.
  skipApprovalScreen: true

staticClients:
  - id: ${OIDC_CLIENT_ID}
    secret: ${OIDC_CLIENT_SECRET}
    name: Antrea UI
    redirectURIs:
      - ${ANTREA_UI_URL}/auth/oauth2/callback

connectors:
  - type: mockCallback
    id: mock
    name: Test
EOF
    chmod 644 "${DEX_DIR}/config.yaml"
}

start_dex() {
    generate_certs
    write_dex_config

    if ! docker network inspect "${KIND_NETWORK}" >/dev/null 2>&1; then
        log "Creating Docker network ${KIND_NETWORK}"
        docker network create "${KIND_NETWORK}" >/dev/null
    fi

    docker rm -f "${DEX_CONTAINER}" >/dev/null 2>&1 || true

    log "Starting Dex (${DEX_IMAGE}) on network ${KIND_NETWORK} as ${DEX_HOSTNAME}"
    # --network-alias is what lets the Kind nodes (and therefore the apiserver) resolve the issuer
    # hostname. The published port is for the e2e test on the host, which cannot reach container
    # IPs on macOS; it is bound to loopback so this does not expose Dex on the machine's network.
    docker run -d --name "${DEX_CONTAINER}" \
        --network "${KIND_NETWORK}" \
        --network-alias "${DEX_HOSTNAME}" \
        -p "127.0.0.1:${DEX_PORT}:${DEX_PORT}" \
        -v "${DEX_DIR}/config.yaml:/etc/dex/config.yaml:ro" \
        -v "${DEX_DIR}/tls.crt:/etc/dex/tls.crt:ro" \
        -v "${DEX_DIR}/tls.key:/etc/dex/tls.key:ro" \
        "${DEX_IMAGE}" dex serve /etc/dex/config.yaml >/dev/null

    log "Waiting for Dex to serve its discovery document"
    for _ in $(seq 1 60); do
        if curl -sf --cacert "${DEX_DIR}/ca.crt" --resolve "${DEX_HOSTNAME}:${DEX_PORT}:127.0.0.1" \
            "https://${DEX_HOSTNAME}:${DEX_PORT}/dex/.well-known/openid-configuration" >/dev/null 2>&1; then
            log "Dex is ready at https://${DEX_HOSTNAME}:${DEX_PORT}/dex"
            return 0
        fi
        sleep 1
    done
    log "ERROR: Dex did not become ready. Container logs:"
    docker logs "${DEX_CONTAINER}" || true
    return 1
}

# dex_ip prints the Dex container's address on the Kind network.
#
# Pods resolve names through CoreDNS, which does not consult Docker's embedded DNS, so the network
# alias that lets the apiserver reach "dex.e2e" does nothing for the backend. Feeding this address
# to the chart's hostAliases value puts the mapping straight into that one Pod's /etc/hosts, which
# is where the Go resolver looks first - no cluster-wide DNS configuration to mutate and nothing
# else to restart.
dex_ip() {
    local ip
    ip=$(docker inspect -f "{{(index .NetworkSettings.Networks \"${KIND_NETWORK}\").IPAddress}}" "${DEX_CONTAINER}")
    if [ -z "${ip}" ]; then
        echo "could not determine the Dex container's IP on network ${KIND_NETWORK}" >&2
        return 1
    fi
    echo "${ip}"
}

configure_cluster() {
    # The backend has to trust Dex's certificate for OIDC discovery and the code exchange.
    # ci/antrea-ui-values.yml mounts this ConfigMap into /etc/ssl/certs.
    log "Publishing the Dex CA as a ConfigMap for the antrea-ui backend"
    kubectl -n kube-system create configmap antrea-ui-e2e-dex-ca \
        --from-file=ca.crt="${DEX_DIR}/ca.crt" \
        --dry-run=client -o yaml | kubectl apply -f - >/dev/null

    # Give the identity Dex hands out the RBAC the UI's own pages need. Binding the *group* rather
    # than the user is deliberate: it proves the groups claim survives the whole chain, from Dex
    # through the id_token to the apiserver's authorizer.
    log "Binding ClusterRole antrea-ui-admin-core to group ${TEST_USER_GROUP}"
    kubectl create clusterrolebinding antrea-ui-e2e-oidc \
        --clusterrole=antrea-ui-admin-core \
        --group="${TEST_USER_GROUP}" \
        --dry-run=client -o yaml | kubectl apply -f - >/dev/null
}

stop() {
    log "Removing Dex container"
    docker rm -f "${DEX_CONTAINER}" >/dev/null 2>&1 || true
}

case "${1:-}" in
    start-dex) start_dex ;;
    configure-cluster) configure_cluster ;;
    dex-ip) dex_ip ;;
    stop) stop ;;
    *)
        echo "Usage: $0 {start-dex|configure-cluster|dex-ip|stop}" >&2
        exit 1
        ;;
esac
