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

# Deploys the support bundle source that test/e2e/supportbundle_test.go collects from: one server
# (test/e2e/supportbundle-source) reached both ways a source can be,
#
#   - as an APIService (v1alpha1.supportbundle.e2e.antrea.io), which the supportbundle-plugin
#     installed by ci/e2e-plugins.sh declares as its apiServer source;
#   - directly over HTTPS, as an extra source configured through the chart values written by
#     configure-cluster.
#
# The server's CA is generated here and never leaves the machine.
#
# Usage (run from the repo root):
#   ci/e2e-supportbundle.sh build              # build the source's image
#   ci/e2e-supportbundle.sh configure-cluster  # run BEFORE installing Antrea UI
#   ci/e2e-supportbundle.sh values-file        # print the chart values file to install with
#   ci/e2e-supportbundle.sh clean

set -eo pipefail

THIS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "${THIS_DIR}")"

IMAGE="antrea/antrea-ui-e2e-supportbundle-source:latest"
# Fixed, so the subcommands can run as separate steps and still find each other's output.
WORK_DIR="/tmp/antrea-ui-e2e-supportbundle"
VALUES_FILE="${WORK_DIR}/values.yml"

NAMESPACE="antrea-ui-e2e-supportbundle"
NAME="supportbundle-source"
SERVICE_HOSTNAME="${NAME}.${NAMESPACE}.svc"
# Matches group and version in test/e2e/supportbundle-source/main.go, and the plugin manifest
# written by ci/e2e-plugins.sh.
GROUP="supportbundle.e2e.antrea.io"
VERSION="v1alpha1"
# Matches extraSourceName in test/e2e/supportbundle_test.go. The source's default
# --https-audience, supportbundle.ui.antrea.io/e2e-https, is derived from it.
EXTRA_SOURCE_NAME="e2e-https"

function log() {
    echo "[e2e-supportbundle] $*"
}

function build() {
    local go_version
    go_version=$(grep '^go ' "${ROOT_DIR}/go.mod" | awk '{print $2}' | cut -d '.' -f 1,2)
    docker build -t "${IMAGE}" -f "${ROOT_DIR}/test/e2e/supportbundle-source/Dockerfile" \
        --build-arg GO_VERSION="${go_version}" "${ROOT_DIR}"
}

function generate_certs() {
    # An openssl.cnf rather than -addext, for the LibreSSL that ships with macOS.
    cat > "${WORK_DIR}/openssl.cnf" <<EOF
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
subjectAltName = DNS:${SERVICE_HOSTNAME}
EOF
    openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
        -keyout "${WORK_DIR}/ca.key" -out "${WORK_DIR}/ca.crt" \
        -subj "/CN=antrea-ui-e2e-supportbundle-ca" \
        -config "${WORK_DIR}/openssl.cnf" -extensions v3_ca 2>/dev/null
    openssl req -newkey rsa:2048 -nodes \
        -keyout "${WORK_DIR}/tls.key" -out "${WORK_DIR}/tls.csr" \
        -subj "/CN=${SERVICE_HOSTNAME}" \
        -config "${WORK_DIR}/openssl.cnf" 2>/dev/null
    openssl x509 -req -in "${WORK_DIR}/tls.csr" -days 3650 \
        -CA "${WORK_DIR}/ca.crt" -CAkey "${WORK_DIR}/ca.key" -CAcreateserial \
        -out "${WORK_DIR}/tls.crt" \
        -extfile "${WORK_DIR}/openssl.cnf" -extensions v3_server 2>/dev/null
    rm -f "${WORK_DIR}/tls.csr"
}

function configure_cluster() {
    generate_certs

    log "Deploying ${NAME} in ${NAMESPACE}"
    kubectl create namespace "${NAMESPACE}"
    kubectl create secret tls "${NAME}-tls" --namespace "${NAMESPACE}" \
        --cert="${WORK_DIR}/tls.crt" --key="${WORK_DIR}/tls.key"

    local ca_bundle
    ca_bundle=$(base64 < "${WORK_DIR}/ca.crt" | tr -d '\n')

    kubectl apply -f - <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ${NAME}
  namespace: ${NAMESPACE}
---
# For the TokenReviews that authenticate requests to the HTTPS endpoint, and the
# SubjectAccessReviews that authorize them.
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: antrea-ui-e2e-${NAME}-auth-delegator
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: system:auth-delegator
subjects:
- kind: ServiceAccount
  name: ${NAME}
  namespace: ${NAMESPACE}
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${NAME}
  namespace: ${NAMESPACE}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: ${NAME}
  template:
    metadata:
      labels:
        app: ${NAME}
    spec:
      serviceAccountName: ${NAME}
      containers:
      - name: ${NAME}
        image: ${IMAGE}
        imagePullPolicy: IfNotPresent
        ports:
        - containerPort: 8443
        readinessProbe:
          httpGet:
            path: /healthz
            port: 8443
            scheme: HTTPS
        volumeMounts:
        - name: tls
          mountPath: /etc/tls
          readOnly: true
      volumes:
      - name: tls
        secret:
          secretName: ${NAME}-tls
---
apiVersion: v1
kind: Service
metadata:
  name: ${NAME}
  namespace: ${NAMESPACE}
spec:
  selector:
    app: ${NAME}
  ports:
  - port: 443
    targetPort: 8443
---
apiVersion: apiregistration.k8s.io/v1
kind: APIService
metadata:
  name: ${VERSION}.${GROUP}
spec:
  group: ${GROUP}
  version: ${VERSION}
  groupPriorityMinimum: 100
  versionPriority: 100
  caBundle: ${ca_bundle}
  service:
    name: ${NAME}
    namespace: ${NAMESPACE}
    port: 443
---
# The apiServer source is called as antrea-ui-admin, whoever requests the bundle. As whoever
# deploys a plugin declaring a source must (here, supportbundle-plugin), grant it access through
# aggregation into the antrea-ui-admin ClusterRole (see docs/plugins.md).
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: antrea-ui-e2e-supportbundle-source
  labels:
    rbac.ui.antrea.io/aggregate-to-antrea-ui-admin: "true"
rules:
- apiGroups: ["${GROUP}"]
  resources: ["supportbundle", "supportbundle/status", "supportbundle/download"]
  verbs: ["create", "get", "delete"]
EOF

    kubectl rollout status --namespace "${NAMESPACE}" "deployment/${NAME}" --timeout=5m
    kubectl wait --for=condition=Available "apiservice/${VERSION}.${GROUP}" --timeout=2m

    log "Writing ${VALUES_FILE}"
    {
        echo "supportBundle:"
        echo "  extraSources:"
        echo "    - name: ${EXTRA_SOURCE_NAME}"
        echo "      https:"
        echo "        url: https://${SERVICE_HOSTNAME}/api/v1"
        echo "        caData: |"
        sed 's/^/          /' "${WORK_DIR}/ca.crt"
    } > "${VALUES_FILE}"
}

function values_file() {
    echo "${VALUES_FILE}"
}

function clean() {
    log "Removing ${NAME} and its CA"
    kubectl delete apiservice "${VERSION}.${GROUP}" --ignore-not-found
    kubectl delete clusterrolebinding "antrea-ui-e2e-${NAME}-auth-delegator" --ignore-not-found
    kubectl delete clusterrole antrea-ui-e2e-supportbundle-source --ignore-not-found
    kubectl delete namespace "${NAMESPACE}" --ignore-not-found
    rm -rf "${WORK_DIR}"
}

mkdir -p "${WORK_DIR}"

case "${1:-}" in
    build) build ;;
    configure-cluster) configure_cluster ;;
    values-file) values_file ;;
    clean) clean ;;
    *)
        echo "Usage: $0 {build|configure-cluster|values-file|clean}" >&2
        exit 1
        ;;
esac
