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
}

function clean() {
    log "Removing the plugins namespace, the key ConfigMap and the throwaway key"
    # Deleting the namespace takes the plugin ConfigMaps with it.
    kubectl delete namespace "${PLUGINS_NAMESPACE}" --ignore-not-found
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
