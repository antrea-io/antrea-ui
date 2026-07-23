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

# Runs once at container startup, before nginx starts. Browsers cannot list a
# directory over HTTP, so we merge every installed plugin's manifest.json into
# a single /etc/plugins/index.json that the frontend fetches at load time.

ME=$(basename $0)
PLUGINS_DIR="${PLUGINS_DIR:-/etc/plugins}"
INDEX_FILE="$PLUGINS_DIR/index.json"

if [ ! -d "$PLUGINS_DIR" ]; then
    echo "$ME: $PLUGINS_DIR does not exist, writing empty plugin index"
    mkdir -p "$PLUGINS_DIR"
    echo "[]" > "$INDEX_FILE"
    exit 0
fi

manifests=$(find "$PLUGINS_DIR" -mindepth 2 -maxdepth 2 -name manifest.json 2>/dev/null)

if [ -z "$manifests" ]; then
    echo "$ME: no plugin manifests found under $PLUGINS_DIR"
    echo "[]" > "$INDEX_FILE"
    exit 0
fi

echo "$ME: found plugin manifests: $manifests"

# Build the JSON array by hand: each manifest.json is already a valid JSON object,
# so we just need to join them with commas. Avoids depending on jq, which isn't
# installed in the nginx-unprivileged base image.
#
# We can't fully JSON-validate each manifest without jq, but we can at least reject
# obviously truncated/malformed ones (missing braces) before they get spliced into the
# array — otherwise one bad manifest.json would produce invalid JSON for the whole index
# and break every plugin, not just the bad one.
{
    echo "["
    first=1
    for manifest in $manifests; do
        trimmed=$(tr -d '[:space:]' < "$manifest")
        case "$trimmed" in
            \{*\})
                ;;
            *)
                echo "$ME: skipping malformed manifest (not a JSON object): $manifest" >&2
                continue
                ;;
        esac
        if [ "$first" != "1" ]; then
            echo ","
        fi
        first=0
        cat "$manifest"
    done
    echo "]"
} > "$INDEX_FILE"
