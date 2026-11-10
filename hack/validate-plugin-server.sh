#!/usr/bin/env bash

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

# Validates the server of an antrea-ui plugin, by sending it the requests the antrea-ui backend
# sends. It covers the two things the backend asks of a plugin's server: its frontend bundle
# ("Downloading the bundle" in docs/plugins.md) and its support bundle source ("The source
# protocol" in docs/supportbundle.md).
#
# It is a sanity check for the common mistakes, against what the documentation states, and is
# kept simple on purpose: it does not copy the rules of the backend, which would drift from it,
# and the backend has the final word. So it can miss something the backend rejects, and, for
# input no real server sends, such as an unencoded space in a URL, fail something it accepts. A
# FAIL is a departure from the documented behavior, and the only result that makes the script
# fail. A WARN is something that may be a problem, an INFO something to know.

set -eo pipefail

function usage() {
    cat <<EOF
Usage: $0 [options] [-- <curl arguments>]

Checks the endpoints a plugin declares in its manifest, or the ones named with options.

Manifest (at most one):
  --configmap <namespace>/<name>   read manifest.json, and bundleURL, from the plugin's ConfigMap
  --manifest <file>                read manifest.json from a local file

Where /apis/<group>/<version> paths are requested:
  (default)                        from kube-apiserver, with the current kubectl context
  --server <url>                   from this URL: a server running outside of Kubernetes, or
                                   reached without going through kube-apiserver
  --antrea-ui-namespace <ns>       send the backend's requests to kube-apiserver as the
                                   ServiceAccounts it uses, given the namespace antrea-ui is
                                   installed in. This checks the RBAC of the plugin too, and
                                   takes the right to impersonate. Without it, they are sent as
                                   the current kubectl user

Endpoints (each one overrides the manifest):
  --bundle-api-path <path>         /apis/<group>/<version> serving the frontend bundle
  --bundle-url <url>               URL serving the frontend bundle
  --bundle-sha256 <digest>         SHA-256 of the frontend bundle
  --supportbundle-api-path <path>  /apis/<group>/<version> serving support bundles

Limits (the defaults are the backend's, which its configuration can change):
  --max-bundle-bytes <n>           largest frontend bundle, a WARN past it (default 10485760)
  --max-source-bytes <n>           largest support bundle, a WARN past it (default 268435456)
  --timeout <seconds>              time given to a support bundle collection (default 600)
  --since <duration>               'since' of the support bundle request, which a request to the
                                   API can set (default: none, as the support bundle page)

Arguments after -- are given to curl for the requests to --server, for instance --cacert, --cert
and --key, or -H.

Examples:
  $0 --configmap antrea-ui-plugins/my-plugin --antrea-ui-namespace kube-system
  $0 --manifest dist/manifest.json --bundle-url http://localhost:8080/bundles/<digest>
  $0 --manifest dist/manifest.json --server https://localhost:8443 -- --cacert ca.crt
  $0 --supportbundle-api-path /apis/foo.example.com/v1
EOF
}

function die() {
    echo "Error: $*" >&2
    exit 1
}

CONFIGMAP=""
MANIFEST_FILE=""
SERVER=""
UI_NAMESPACE=""
BUNDLE_API_PATH=""
BUNDLE_URL=""
BUNDLE_SHA256=""
SUPPORTBUNDLE_API_PATH=""
MAX_BUNDLE_BYTES=$((10 * 1024 * 1024))
MAX_SOURCE_BYTES=$((256 * 1024 * 1024))
TIMEOUT=600
SINCE=""
CURL_ARGS=()

while [[ $# -gt 0 ]]; do
    case "$1" in
        -h|--help) usage; exit 0 ;;
        --) shift; CURL_ARGS=("$@"); break ;;
        --configmap) CONFIGMAP="$2" ;;
        --manifest) MANIFEST_FILE="$2" ;;
        --server) SERVER="${2%/}" ;;
        --antrea-ui-namespace) UI_NAMESPACE="$2" ;;
        --bundle-api-path) BUNDLE_API_PATH="${2%/}" ;;
        --bundle-url) BUNDLE_URL="$2" ;;
        --bundle-sha256) BUNDLE_SHA256="$2" ;;
        --supportbundle-api-path) SUPPORTBUNDLE_API_PATH="${2%/}" ;;
        --max-bundle-bytes) MAX_BUNDLE_BYTES="$2" ;;
        --max-source-bytes) MAX_SOURCE_BYTES="$2" ;;
        --timeout) TIMEOUT="$2" ;;
        --since) SINCE="$2" ;;
        *) usage >&2; die "unknown option $1" ;;
    esac
    [[ $# -ge 2 ]] || die "$1 takes a value"
    shift 2
done

[ -z "$CONFIGMAP" ] || [ -z "$MANIFEST_FILE" ] || die "--configmap and --manifest are exclusive"
[ -z "$BUNDLE_API_PATH" ] || [ -z "$BUNDLE_URL" ] || die "--bundle-api-path and --bundle-url are exclusive"
[ -z "$SERVER" ] || [ -z "$UI_NAMESPACE" ] ||
    die "--antrea-ui-namespace is for requests to kube-apiserver, not to --server"
[ -n "$SERVER" ] || [ ${#CURL_ARGS[@]} -eq 0 ] || die "the curl arguments after -- are for requests to --server"

function require() {
    command -v "$1" >/dev/null 2>&1 || command -v "${2:-$1}" >/dev/null 2>&1 ||
        die "$1 ${2:+or $2 }is required but not installed"
}

require curl
require jq

MANIFEST="{}"
CONFIGMAP_BUNDLE_URL=""
if [ -n "$CONFIGMAP" ]; then
    [[ "$CONFIGMAP" == */* ]] || die "--configmap takes <namespace>/<name>"
    require kubectl
    CONFIGMAP_JSON=$(kubectl get configmap --namespace "${CONFIGMAP%%/*}" "${CONFIGMAP#*/}" -o json)
    MANIFEST=$(jq -r '.data["manifest.json"] // (.binaryData["manifest.json"] // "" | @base64d)' <<< "$CONFIGMAP_JSON")
    [ -n "$MANIFEST" ] || die "ConfigMap $CONFIGMAP has no manifest.json"
    # The x keeps the newline the value may end with, which the backend does not accept in a URL.
    CONFIGMAP_BUNDLE_URL=$(jq -j '.data.bundleURL // ""' <<< "$CONFIGMAP_JSON"; echo x)
    CONFIGMAP_BUNDLE_URL="${CONFIGMAP_BUNDLE_URL%x}"
elif [ -n "$MANIFEST_FILE" ]; then
    MANIFEST=$(cat "$MANIFEST_FILE")
fi

function manifest_field() {
    jq -r "$1 // empty" <<< "$MANIFEST"
}

BUNDLE_SHA256="${BUNDLE_SHA256:-$(manifest_field .bundleSha256)}"
SUPPORTBUNDLE_API_PATH="${SUPPORTBUNDLE_API_PATH:-$(manifest_field .supportBundle.apiServer.path)}"
if [ -z "$BUNDLE_API_PATH" ] && [ -z "$BUNDLE_URL" ]; then
    [ -z "$(manifest_field .bundleSource.apiServer)" ] || [ -z "$(manifest_field .bundleSource.http)" ] ||
        die "the manifest's bundleSource sets both apiServer and http, which the backend rejects"
    BUNDLE_API_PATH=$(manifest_field .bundleSource.apiServer.path)
    if [ -n "$(manifest_field .bundleSource.http)" ]; then
        BUNDLE_URL="$CONFIGMAP_BUNDLE_URL"
        if [ -z "$BUNDLE_URL" ] && [ -n "$CONFIGMAP" ]; then
            die "the manifest selects the http transport, and ConfigMap $CONFIGMAP has no bundleURL," \
                "which the backend requires"
        fi
        [ -n "$BUNDLE_URL" ] || die "the manifest selects the http transport: give the bundle's URL with --bundle-url"
    fi
fi

[ -n "${BUNDLE_API_PATH}${BUNDLE_URL}${SUPPORTBUNDLE_API_PATH}" ] ||
    die "nothing to validate: no bundleSource or supportBundle in the manifest, and no endpoint option (see --help)"
if [ -n "${BUNDLE_API_PATH}${BUNDLE_URL}" ]; then
    # The digest is the name of the object in an aggregated API. A URL can be checked without it.
    BUNDLE_SHA256=$(tr 'A-F' 'a-f' <<< "$BUNDLE_SHA256")
    if [ -n "${BUNDLE_SHA256}${BUNDLE_API_PATH}" ] && ! [[ "$BUNDLE_SHA256" =~ ^[0-9a-f]{64}$ ]]; then
        die "the bundle's SHA-256 (bundleSha256 in the manifest, or --bundle-sha256) must be 64 hexadecimal characters"
    fi
    require sha256sum shasum
fi

TMP_DIR=$(mktemp -d)
BODY="$TMP_DIR/body"
PROXY_PID=""
trap '[ -z "$PROXY_PID" ] || kill "$PROXY_PID" 2>/dev/null || true; rm -rf "$TMP_DIR"' EXIT

# Where /apis/... paths are requested, with the curl arguments and the identities that takes. For
# kube-apiserver that is a kubectl proxy, so that curl makes every request of this script,
# whichever way kubectl authenticates. It listens on a socket only this user can open.
API_BASE="$SERVER"
API_ARGS=("${CURL_ARGS[@]}")
BUNDLE_IDENTITY=()
SUPPORTBUNDLE_IDENTITY=()
if [ -z "$SERVER" ] && [ -n "${BUNDLE_API_PATH}${SUPPORTBUNDLE_API_PATH}" ]; then
    require kubectl
    kubectl proxy --unix-socket="$TMP_DIR/proxy.sock" > "$TMP_DIR/proxy.log" 2>&1 &
    PROXY_PID=$!
    for _ in $(seq 100); do
        if [ -S "$TMP_DIR/proxy.sock" ] || ! kill -0 "$PROXY_PID" 2>/dev/null; then break; fi
        sleep 0.1
    done
    [ -S "$TMP_DIR/proxy.sock" ] || die "kubectl proxy did not start: $(cat "$TMP_DIR/proxy.log")"
    API_BASE="http://localhost"
    API_ARGS=(--unix-socket "$TMP_DIR/proxy.sock")
    if [ -n "$UI_NAMESPACE" ]; then
        # The backend downloads a frontend bundle as its own ServiceAccount, and collects a
        # support bundle as antrea-ui-admin, with the requester and its bundle as user extras.
        BUNDLE_IDENTITY=(-H "Impersonate-User: system:serviceaccount:${UI_NAMESPACE}:antrea-ui")
        SUPPORTBUNDLE_IDENTITY=(
            -H "Impersonate-User: system:serviceaccount:${UI_NAMESPACE}:antrea-ui-admin"
            -H "Impersonate-Extra-supportbundle.ui.antrea.io%2Frequested-by: validate-plugin-server"
            -H "Impersonate-Extra-supportbundle.ui.antrea.io%2Fbundle-id: validate-plugin-server-$$"
        )
    else
        echo "INFO  requests go to kube-apiserver as the current kubectl user, so the RBAC antrea-ui" \
            "needs is not checked (see --antrea-ui-namespace)"
    fi
fi

FAILED=0

function pass() {
    echo "PASS  $*"
}

function info() {
    echo "INFO  $*"
}

function warn() {
    echo "WARN  $*"
}

function fail() {
    echo "FAIL  $*"
    FAILED=1
}

# request METHOD URL [curl argument...] sends a request, and follows no redirect. It sets STATUS
# to the response's status (000 without a response) and CURL_EXIT to curl's exit code, and leaves
# the response's body in $BODY. With -q, curl does not read the options of a ~/.curlrc, and with
# -g, it sends the brackets and braces of a URL as they are instead of expanding them.
function request() {
    local method="$1" url="$2"
    shift 2
    : > "$BODY"
    CURL_EXIT=0
    STATUS=$(curl -q -g -sS -X "$method" -o "$BODY" -w '%{http_code}' "$@" --url "$url") ||
        CURL_EXIT=$?
}

# Completes the failure of a request to kube-apiserver that RBAC may explain.
function rbac_hint() {
    if [ "$STATUS" == 403 ] && [ -n "$UI_NAMESPACE" ]; then
        echo "      is the RBAC the plugin has to ship in place? See $1"
    fi
}

function file_size() {
    wc -c < "$1" | tr -d ' '
}

# sha256sum on Linux, shasum -a 256 on macOS.
function sha256() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | cut -d' ' -f1
    else
        shasum -a 256 "$1" | cut -d' ' -f1
    fi
}

# The content of the bundle is not checked: the SHA-256 ties it to the manifest it was built with,
# so the server has no say in it.
function check_bundle() {
    # The backend asks for gzip, and decompresses the response if the server applies it.
    local url="$BUNDLE_URL" args=(--compressed -H 'Accept-Encoding: gzip') size hint digest
    local control='[[:cntrl:]]'
    if [ -n "$BUNDLE_API_PATH" ]; then
        url="${API_BASE}${BUNDLE_API_PATH}/uipluginbundles/${BUNDLE_SHA256}/download"
        args+=("${API_ARGS[@]}" "${BUNDLE_IDENTITY[@]}")
    fi

    echo "Frontend bundle: GET ${BUNDLE_URL:-${url#"$API_BASE"}}"
    if [[ "$BUNDLE_URL" =~ $control ]]; then
        fail "the URL holds a control character, such as a newline at its end"
        return
    fi
    # As the backend does, which also keeps curl from speaking any other protocol it supports,
    # file:// included.
    if [ -n "$BUNDLE_URL" ] && ! [[ "$BUNDLE_URL" =~ ^[Hh][Tt][Tt][Pp]([Ss])?:// ]]; then
        fail "the URL is not an http or https URL, which the backend requires"
        return
    fi
    request GET "$url" --max-time 120 "${args[@]}"
    if [ "$CURL_EXIT" != 0 ]; then
        fail "no complete response (curl exit code $CURL_EXIT)"
        if [ -z "$BUNDLE_API_PATH" ]; then
            echo "      for an http URL only reachable from inside the cluster: kubectl port-forward to" \
                "its Service, then --bundle-url http://localhost:<port>/<same path>"
        fi
        return
    elif [[ "$STATUS" != 2* ]]; then
        hint=""
        [[ "$STATUS" != 3* ]] || hint=" (a redirect is not followed)"
        fail "status $STATUS, want 2xx$hint: $(head -c 300 "$BODY")"
        [ -z "$BUNDLE_API_PATH" ] || rbac_hint "\"What the aggregated API server has to provide\" in docs/plugins.md"
        return
    fi
    size=$(file_size "$BODY")
    pass "status $STATUS, $size bytes"
    if [ "$size" -gt "$MAX_BUNDLE_BYTES" ]; then
        warn "larger than $MAX_BUNDLE_BYTES bytes, which the backend refuses unless plugins.maxBundleBytes" \
            "is larger (see --max-bundle-bytes)"
    fi

    digest=$(sha256 "$BODY")
    if [ -z "$BUNDLE_SHA256" ]; then
        info "no bundleSha256 to compare with: the SHA-256 of the bundle is $digest"
    elif [ "$digest" == "$BUNDLE_SHA256" ]; then
        pass "SHA-256 is $digest"
    else
        fail "SHA-256 is $digest, want $BUNDLE_SHA256"
    fi
}

# wait_for_support_bundle URL [curl argument...] polls the bundle at URL until it is collected,
# then downloads it. It polls every 2s, as the backend does when the source gives no Retry-After.
function wait_for_support_bundle() {
    local url="$1" deadline=$((SECONDS + TIMEOUT)) phase size
    shift
    while true; do
        sleep 2
        if [ "$SECONDS" -ge "$deadline" ]; then
            fail "the bundle is not collected after ${TIMEOUT}s"
            return
        fi
        request GET "$url" --max-time 30 "$@"
        # What the backend takes for a brief unavailability, and polls through.
        if [ "$CURL_EXIT" != 0 ] || [[ "$STATUS" =~ ^(429|502|503|504)$ ]]; then
            continue
        elif [ "$STATUS" != 200 ]; then
            fail "GET of the bundle: status $STATUS, want 200"
            rbac_hint "\"Support bundle sources\" in docs/plugins.md"
            return
        # With -n and input, jq reads the first JSON value only, as the backend does.
        elif ! phase=$(jq -ner 'input | .status.phase // "" | if . == "" then "Collecting" else . end' \
                "$BODY" 2>/dev/null); then
            fail "GET of the bundle: the response is not a JSON object"
            return
        fi
        case "$phase" in
            Collecting) ;;
            Collected) break ;;
            Failed)
                fail "the source failed to collect the bundle: $(jq -nr 'input | .status.message // empty' "$BODY")"
                return
                ;;
            *)
                fail "unknown phase \"$phase\", want Collecting, Collected or Failed"
                return
                ;;
        esac
    done
    pass "phase Collected after $((SECONDS + TIMEOUT - deadline))s"

    # The download has what is left of the time given to the collection.
    if [ "$SECONDS" -ge "$deadline" ]; then
        fail "download: no time left of the ${TIMEOUT}s"
        return
    fi
    request GET "$url/download" --max-time $((deadline - SECONDS)) "$@"
    if [ "$CURL_EXIT" != 0 ]; then
        fail "download: no complete response (curl exit code $CURL_EXIT)"
    elif [ "$STATUS" != 200 ]; then
        fail "download: status $STATUS, want 200"
        rbac_hint "\"Support bundle sources\" in docs/plugins.md"
    else
        size=$(file_size "$BODY")
        pass "download: status 200, $size bytes"
        if [ "$size" -gt "$MAX_SOURCE_BYTES" ]; then
            warn "download: larger than $MAX_SOURCE_BYTES bytes, which the backend refuses unless" \
                "supportBundle.maxSourceBytes is larger (see --max-source-bytes)"
        fi
        # Not checked by the backend, which stores the download as <plugin name>.tar.gz.
        tar -tzf "$BODY" >/dev/null 2>&1 || warn "download: not a gzipped tarball"
    fi
}

function check_support_bundle() {
    local url="${API_BASE}${SUPPORTBUNDLE_API_PATH}/supportbundles" name
    local args=(-H 'Accept-Encoding: identity' "${API_ARGS[@]}" "${SUPPORTBUNDLE_IDENTITY[@]}")
    local bundle='{"metadata": {"generateName": "antrea-ui-"}}'
    if [ -n "$SINCE" ]; then
        bundle=$(jq -c --arg since "$SINCE" '.spec = {since: $since}' <<< "$bundle")
    fi

    echo "Support bundle: POST ${url#"$API_BASE"}"
    request POST "$url" --max-time 30 -H 'Content-Type: application/json' -d "$bundle" "${args[@]}"
    if [ "$CURL_EXIT" != 0 ]; then
        fail "no response (curl exit code $CURL_EXIT)"
        return
    elif ! [[ "$STATUS" =~ ^(200|201|202)$ ]]; then
        fail "status $STATUS, want 200, 201 or 202: $(head -c 300 "$BODY")"
        rbac_hint "\"Support bundle sources\" in docs/plugins.md"
        return
    fi
    name=$(jq -nr 'input | .metadata.name // empty' "$BODY" 2>/dev/null) || true
    if ! [[ "$name" =~ ^[A-Za-z0-9._-]{1,128}$ ]] || [ "$name" == "." ] || [ "$name" == ".." ]; then
        fail "status $STATUS, but the response has no usable metadata.name (got \"$name\")"
        return
    fi
    pass "status $STATUS, the bundle is $name"

    wait_for_support_bundle "$url/$name" "${args[@]}"

    # Sent whatever happened to the bundle, as the backend does. The backend only logs a failure.
    request DELETE "$url/$name" --max-time 10 "${args[@]}"
    if [[ "$STATUS" =~ ^(200|204|404)$ ]]; then
        pass "DELETE: status $STATUS"
    else
        warn "DELETE: status $STATUS, want 200, 204 or 404: the source may keep the bundle"
    fi
}

if [ -n "${BUNDLE_API_PATH}${BUNDLE_URL}" ]; then
    check_bundle
else
    echo "Frontend bundle: not downloaded from a server, skipped"
fi
if [ -n "$SUPPORTBUNDLE_API_PATH" ]; then
    check_support_bundle
else
    echo "Support bundle: no source, skipped"
fi

if [ "$FAILED" != 0 ]; then
    echo "The server does not do what the antrea-ui backend expects"
    exit 1
fi
echo "No problem found with the server"
