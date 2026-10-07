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

# Prepares the plugins that the plugin e2e tests (test/e2e/plugin_test.go) run against.
#
# The e2e deployment turns plugin signature verification on (see ci/antrea-ui-values.yml), so the
# pod-counter example plugin has to be signed or the backend will refuse to load it. Two plugins
# are installed, covering both halves of that:
#
#   - pod-counter:          signed, and expected to load (TestPluginLoading)
#   - no-signature-plugin:  structurally valid but carrying no manifest.json.asc, and expected to
#                           be rejected (TestPluginWithoutSignatureIsRejected)
#
# Three more cover a plugin whose bundle is downloaded instead of shipped in its ConfigMap (see
# "Downloading the bundle" in docs/plugins.md). A stub bundle server (ci/e2e-bundle-server.py)
# serves the bundle two ways. Over TLS it is an aggregated API server registered with an
# APIService, so requests go through kube-apiserver for real, and the backend's ServiceAccount is
# granted - or, for the denied plugin, not granted - access to its bundles. Over plain HTTP it is
# an ordinary Service:
#
#   - remote-plugin:         through kube-apiserver, expected to load (TestPluginBundleDownload)
#   - remote-plugin-denied:  the same, but the ServiceAccount has no RBAC for the resource it
#                            names, so kube-apiserver refuses with a 403 and the backend keeps
#                            retrying (TestPluginBundleDownloadWithoutRBAC)
#   - http-plugin:           over plain HTTP to the Service, the URL being in the ConfigMap,
#                            expected to load (TestPluginBundleDownloadOverHTTP)
#
# The signing key is generated here into a throwaway GNUPGHOME and never leaves the machine; no
# key material is committed.
#
# The plugin ConfigMaps go in a dedicated namespace, PLUGINS_NAMESPACE, rather than the release
# namespace, which holds the backend configuration and the trusted key ConfigMap. configure-cluster
# has to run BEFORE Antrea UI is installed: the chart creates a Role/RoleBinding in the plugins
# namespace, and the backend only reads the key ring at startup.
#
# Usage (run from the repo root, after `npm run build` in plugins/examples/pod-counter):
#   ci/e2e-plugins.sh sign               # generate a key and sign the built plugin
#   ci/e2e-plugins.sh configure-cluster  # run BEFORE installing Antrea UI
#   ci/e2e-plugins.sh create-plugins     # run after Antrea UI is up
#   ci/e2e-plugins.sh clean              # drop the ConfigMaps, the plugins namespace and the key

set -eo pipefail

THIS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "${THIS_DIR}")"

# Where the example plugin's `npm run build` leaves manifest.json and bundle.zip.
PLUGIN_DIST="${ROOT_DIR}/plugins/examples/pod-counter/dist"
# Fixed paths, not mktemp ones, so the subcommands above can run as separate steps (separate
# shells) and still find each other's output.
WORK_DIR="/tmp/antrea-ui-e2e-plugins"
PUBLIC_KEY="${WORK_DIR}/public-key.asc"
export GNUPGHOME="${WORK_DIR}/gnupg"

# The namespace Antrea UI is installed into, where the trusted key ConfigMap has to be (the backend
# Pod can only mount ConfigMaps from its own namespace). Overridable so this works against a
# non-default install.
RELEASE_NAMESPACE="${RELEASE_NAMESPACE:-kube-system}"
# The namespace Antrea UI watches for plugin ConfigMaps. Matches plugins.namespace in
# ci/antrea-ui-values.yml.
PLUGINS_NAMESPACE="${PLUGINS_NAMESPACE:-antrea-ui-plugins}"

# Only ever exists in a throwaway test cluster.
KEY_NAME="antrea-ui e2e plugin signing"
# Matches plugins.signature.trustedKeys in ci/antrea-ui-values.yml.
KEY_CONFIGMAP="antrea-ui-plugin-keys"
KEY_CONFIGMAP_KEY="public-key.asc"

# The stub bundle server's API group (matching ci/e2e-bundle-server.py) and the name its objects
# share. It lives in the plugins namespace, which is deleted by clean.
BUNDLE_API_GROUP="ui.e2e.antrea.io"
BUNDLE_SERVER="e2e-bundle-server"
BUNDLE_PATH="/apis/${BUNDLE_API_GROUP}/v1/uipluginbundles"
# A resource of the same group the ServiceAccount is not granted access to.
DENIED_BUNDLE_PATH="/apis/${BUNDLE_API_GROUP}/v1/deniedbundles"

function log() {
    echo "[e2e-plugins] $*"
}

function sign() {
    if [ ! -f "${PLUGIN_DIST}/bundle.zip" ]; then
        echo "Error: ${PLUGIN_DIST}/bundle.zip not found - build the example plugin first:" >&2
        echo "  (cd plugins/examples/pod-counter && npm ci && npm run build)" >&2
        exit 1
    fi

    rm -rf "${GNUPGHOME}"
    mkdir -p "${GNUPGHOME}"
    chmod 700 "${GNUPGHOME}"

    log "Generating a throwaway signing key"
    # No passphrase: this key is generated, used, and discarded within one CI job.
    gpg --batch --pinentry-mode loopback --passphrase '' \
        --quick-generate-key "${KEY_NAME}" default default never
    gpg --armor --export "${KEY_NAME}" > "${PUBLIC_KEY}"

    log "Signing ${PLUGIN_DIST}"
    # Writes bundle.zip's digest into manifest.json and only then signs it - that order is the
    # whole reason this is a script rather than two commands (see docs/plugins.md).
    "${ROOT_DIR}/hack/sign-plugin.sh" "${PLUGIN_DIST}"
}

function configure_cluster() {
    log "Creating namespace ${PLUGINS_NAMESPACE}"
    # Must exist before the chart is installed, which creates the backend's Role/RoleBinding for
    # reading plugin ConfigMaps in it.
    kubectl create namespace "${PLUGINS_NAMESPACE}"

    log "Creating ConfigMap ${KEY_CONFIGMAP} in ${RELEASE_NAMESPACE}"
    # Must exist before the backend starts: it loads the key ring once, at startup, and fails the
    # process if it cannot. Not created by the chart - a trust anchor doesn't belong in
    # values.yaml.
    kubectl create configmap "${KEY_CONFIGMAP}" --namespace "${RELEASE_NAMESPACE}" \
        --from-file="${KEY_CONFIGMAP_KEY}=${PUBLIC_KEY}"
}

# Deploys the stub aggregated API server, registers it with kube-apiserver and grants the
# antrea-ui ServiceAccount read access to uipluginbundles (and only that).
function create_bundle_server() {
    log "Deploying the stub bundle server"
    local dir="${WORK_DIR}/bundle-server"
    mkdir -p "${dir}"
    # kube-apiserver's connection to an aggregated API server is TLS; the APIService below skips
    # verifying it, so the certificate only has to exist.
    openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj "/CN=${BUNDLE_SERVER}" \
        -keyout "${dir}/tls.key" -out "${dir}/tls.crt" 2>/dev/null
    kubectl create secret tls "${BUNDLE_SERVER}-tls" --namespace "${PLUGINS_NAMESPACE}" \
        --cert="${dir}/tls.crt" --key="${dir}/tls.key"
    kubectl create configmap "${BUNDLE_SERVER}-script" --namespace "${PLUGINS_NAMESPACE}" \
        --from-file="${THIS_DIR}/e2e-bundle-server.py"
    kubectl create configmap "${BUNDLE_SERVER}-bundle" --namespace "${PLUGINS_NAMESPACE}" \
        --from-file="${PLUGIN_DIST}/bundle.zip"
    kubectl apply -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${BUNDLE_SERVER}
  namespace: ${PLUGINS_NAMESPACE}
spec:
  replicas: 1
  selector:
    matchLabels:
      app: ${BUNDLE_SERVER}
  template:
    metadata:
      labels:
        app: ${BUNDLE_SERVER}
    spec:
      containers:
      - name: server
        image: python:3.13-alpine
        command: ["python", "-u", "/script/e2e-bundle-server.py"]
        ports:
        - containerPort: 8443
        - containerPort: 8080
        readinessProbe:
          tcpSocket:
            port: 8443
        volumeMounts:
        - {name: script, mountPath: /script}
        - {name: bundle, mountPath: /bundle}
        - {name: tls, mountPath: /tls}
      volumes:
      - name: script
        configMap:
          name: ${BUNDLE_SERVER}-script
      - name: bundle
        configMap:
          name: ${BUNDLE_SERVER}-bundle
      - name: tls
        secret:
          secretName: ${BUNDLE_SERVER}-tls
---
apiVersion: v1
kind: Service
metadata:
  name: ${BUNDLE_SERVER}
  namespace: ${PLUGINS_NAMESPACE}
spec:
  selector:
    app: ${BUNDLE_SERVER}
  ports:
  - name: https
    port: 443
    targetPort: 8443
  - name: http
    port: 8080
    targetPort: 8080
---
apiVersion: apiregistration.k8s.io/v1
kind: APIService
metadata:
  name: v1.${BUNDLE_API_GROUP}
spec:
  group: ${BUNDLE_API_GROUP}
  version: v1
  groupPriorityMinimum: 1000
  versionPriority: 15
  insecureSkipTLSVerify: true
  service:
    name: ${BUNDLE_SERVER}
    namespace: ${PLUGINS_NAMESPACE}
    port: 443
---
# What docs/plugins.md tells a plugin provider's chart to ship. Deliberately nothing for deniedbundles.
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: ${BUNDLE_SERVER}-reader
rules:
- apiGroups: ["${BUNDLE_API_GROUP}"]
  resources: ["uipluginbundles"]
  verbs: ["get", "list"]
- apiGroups: ["${BUNDLE_API_GROUP}"]
  resources: ["uipluginbundles/download"]
  verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: ${BUNDLE_SERVER}-reader
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: ${BUNDLE_SERVER}-reader
subjects:
- kind: ServiceAccount
  name: antrea-ui
  namespace: ${RELEASE_NAMESPACE}
EOF
    kubectl rollout status --namespace "${PLUGINS_NAMESPACE}" "deployment/${BUNDLE_SERVER}" --timeout=5m
    # The plugin ConfigMaps are created only once the APIService answers, so the tests don't
    # depend on the backend's retry to get past the aggregation layer coming up.
    kubectl wait --for=condition=Available "apiservice/v1.${BUNDLE_API_GROUP}" --timeout=3m
}

# create_remote_plugin NAME PATH creates a signed, manifest-only plugin ConfigMap named NAME,
# whose bundle (the pod-counter one, as the digest in its manifest says) is downloaded from PATH.
function create_remote_plugin() {
    local name="$1" path="$2"
    local dir="${WORK_DIR}/${name}"
    mkdir -p "${dir}"
    jq --arg n "${name}" --arg p "${path}" \
        '.name = $n | .bundleSource = {apiServer: {path: $p}}' \
        "${PLUGIN_DIST}/manifest.json" > "${dir}/manifest.json"
    # The only key in this GNUPGHOME is the one sign generated.
    gpg --detach-sign --armor --yes --output "${dir}/manifest.json.asc" "${dir}/manifest.json"
    kubectl create configmap "${name}" --namespace "${PLUGINS_NAMESPACE}" \
        --from-file="${dir}/manifest.json" \
        --from-file="${dir}/manifest.json.asc"
    kubectl label configmap "${name}" --namespace "${PLUGINS_NAMESPACE}" ui.antrea.io/plugin=true
}

# create_http_plugin NAME creates a signed, manifest-only plugin ConfigMap named NAME, whose bundle
# is downloaded over plain HTTP from the stub server's Service. The URL is in the ConfigMap, not
# the signed manifest.
function create_http_plugin() {
    local name="$1"
    local dir="${WORK_DIR}/${name}"
    mkdir -p "${dir}"
    jq --arg n "${name}" '.name = $n | .bundleSource = {http: {}}' \
        "${PLUGIN_DIST}/manifest.json" > "${dir}/manifest.json"
    gpg --detach-sign --armor --yes --output "${dir}/manifest.json.asc" "${dir}/manifest.json"
    local digest
    digest="$(jq -r '.bundleSha256' "${dir}/manifest.json")"
    kubectl create configmap "${name}" --namespace "${PLUGINS_NAMESPACE}" \
        --from-file="${dir}/manifest.json" \
        --from-file="${dir}/manifest.json.asc" \
        --from-literal="bundleURL=http://${BUNDLE_SERVER}.${PLUGINS_NAMESPACE}.svc:8080/bundles/${digest}"
    kubectl label configmap "${name}" --namespace "${PLUGINS_NAMESPACE}" ui.antrea.io/plugin=true
}

function create_plugins() {
    # Creation order between the two doesn't matter: TestPluginWithoutSignatureIsRejected
    # waits for the backend to log its rejection of this ConfigMap before asserting the
    # plugin's absence, rather than inferring that the load attempt happened from the other
    # plugin's presence. It does match on this ConfigMap's name, though, so renaming it means
    # renaming noSignaturePluginName in test/e2e/plugin_test.go too.
    log "Creating the no-signature plugin ConfigMap (expected to be rejected)"
    local no_signature_dir="${WORK_DIR}/no-signature-plugin"
    rm -rf "${no_signature_dir}"
    mkdir -p "${no_signature_dir}"
    cp "${PLUGIN_DIST}/bundle.zip" "${no_signature_dir}/bundle.zip"
    # Same bundle, a different plugin name, and deliberately no manifest.json.asc.
    jq '.name = "no-signature-plugin"' "${PLUGIN_DIST}/manifest.json" > "${no_signature_dir}/manifest.json"
    kubectl create configmap no-signature-plugin --namespace "${PLUGINS_NAMESPACE}" \
        --from-file="${no_signature_dir}/bundle.zip" \
        --from-file="${no_signature_dir}/manifest.json"
    kubectl label configmap no-signature-plugin --namespace "${PLUGINS_NAMESPACE}" ui.antrea.io/plugin=true

    log "Creating the pod-counter plugin ConfigMap (signed)"
    # The same mechanism a real plugin deployment uses (see docs/plugins.md) - the backend's
    # ConfigMap watch picks it up with no antrea-ui restart. manifest.json.asc is a third key
    # alongside the usual two, since this deployment requires signed plugins.
    kubectl create configmap pod-counter-plugin --namespace "${PLUGINS_NAMESPACE}" \
        --from-file="${PLUGIN_DIST}/bundle.zip" \
        --from-file="${PLUGIN_DIST}/manifest.json" \
        --from-file="${PLUGIN_DIST}/manifest.json.asc"
    kubectl label configmap pod-counter-plugin --namespace "${PLUGINS_NAMESPACE}" ui.antrea.io/plugin=true
    # Grants the plugin's own RBAC; without it the plugin's page loads but its K8s call gets a 403.
    kubectl apply -f "${ROOT_DIR}/plugins/examples/pod-counter/clusterrole.yaml"

    create_bundle_server
    # The ConfigMap names are matched by TestPluginBundleDownload,
    # TestPluginBundleDownloadOverHTTP and TestPluginBundleDownloadWithoutRBAC in
    # test/e2e/plugin_test.go.
    log "Creating the remote-plugin ConfigMap (bundle downloaded, expected to load)"
    create_remote_plugin remote-plugin "${BUNDLE_PATH}"
    log "Creating the http-plugin ConfigMap (bundle downloaded over plain HTTP)"
    create_http_plugin http-plugin
    log "Creating the remote-plugin-denied ConfigMap (download refused by RBAC)"
    create_remote_plugin remote-plugin-denied "${DENIED_BUNDLE_PATH}"
}

function clean() {
    log "Removing the plugins namespace, the key ConfigMap and the throwaway key"
    # Deleting the namespace takes the plugin ConfigMaps with it.
    kubectl delete namespace "${PLUGINS_NAMESPACE}" --ignore-not-found
    kubectl delete apiservice "v1.${BUNDLE_API_GROUP}" --ignore-not-found
    kubectl delete clusterrolebinding,clusterrole "${BUNDLE_SERVER}-reader" --ignore-not-found
    kubectl delete configmap "${KEY_CONFIGMAP}" --namespace "${RELEASE_NAMESPACE}" --ignore-not-found
    rm -rf "${WORK_DIR}"
}

mkdir -p "${WORK_DIR}"

case "${1:-}" in
    sign) sign ;;
    configure-cluster) configure_cluster ;;
    create-plugins) create_plugins ;;
    clean) clean ;;
    *)
        echo "Usage: $0 {sign|configure-cluster|create-plugins|clean}" >&2
        exit 1
        ;;
esac
