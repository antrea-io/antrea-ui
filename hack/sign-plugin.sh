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

# Signs a built antrea-ui frontend plugin so a backend with plugin signature verification
# enabled will load it. See docs/plugins.md.
#
# The step order here is the whole point of this being a script rather than three commands in
# the docs: bundle.zip's digest has to be written into manifest.json *before* manifest.json is
# signed. Sign first and you get a plugin that looks perfectly well-formed and fails
# verification at load time, with the manifest's bundleSha256 outside what the signature covers.

set -eo pipefail

function usage() {
    echo "Usage: $0 <plugin-dir> [gpg-key-id]"
    echo "  <plugin-dir>  directory holding the built manifest.json and bundle.zip"
    echo "                (e.g. plugins/examples/pod-counter/dist after 'npm run build')"
    echo "  [gpg-key-id]  key to sign with; defaults to gpg's default signing key"
    echo
    echo "Writes bundle.zip's SHA-256 into manifest.json's 'bundleSha256' and then writes a"
    echo "detached armored signature over manifest.json to manifest.json.asc. Re-running on an"
    echo "already-signed directory is safe: the digest is set, not appended to, and the"
    echo "signature is overwritten."
}

PLUGIN_DIR="$1"
KEY_ID="$2"

if [ -z "$PLUGIN_DIR" ] || [ "$PLUGIN_DIR" == "-h" ] || [ "$PLUGIN_DIR" == "--help" ]; then
    usage
    exit 1
fi

MANIFEST="$PLUGIN_DIR/manifest.json"
BUNDLE="$PLUGIN_DIR/bundle.zip"

for f in "$MANIFEST" "$BUNDLE"; do
    if [ ! -f "$f" ]; then
        echo "Error: $f not found - build the plugin first (npm run build)" >&2
        exit 1
    fi
done

for cmd in jq gpg; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "Error: $cmd is required but not installed" >&2
        exit 1
    fi
done

# sha256sum on Linux, shasum -a 256 on macOS.
if command -v sha256sum >/dev/null 2>&1; then
    DIGEST=$(sha256sum "$BUNDLE" | cut -d' ' -f1)
elif command -v shasum >/dev/null 2>&1; then
    DIGEST=$(shasum -a 256 "$BUNDLE" | cut -d' ' -f1)
else
    echo "Error: neither sha256sum nor shasum found" >&2
    exit 1
fi

# jq reformats manifest.json as it writes it back. That is fine, and is exactly why the signing
# step below has to come after this one: the signature covers the file's exact bytes.
TMP_MANIFEST=$(mktemp)
trap 'rm -f "$TMP_MANIFEST"' EXIT
jq --arg d "$DIGEST" '.bundleSha256 = $d' "$MANIFEST" > "$TMP_MANIFEST"
# Written back through the existing file rather than `mv`, which would hand manifest.json
# mktemp's 0600 mode and a new inode.
cat "$TMP_MANIFEST" > "$MANIFEST"
echo "Set bundleSha256 to $DIGEST in $MANIFEST"

GPG_ARGS=(--detach-sign --armor --yes --output "$MANIFEST.asc")
if [ -n "$KEY_ID" ]; then
    GPG_ARGS+=(--local-user "$KEY_ID")
fi
gpg "${GPG_ARGS[@]}" "$MANIFEST"
echo "Wrote $MANIFEST.asc"
