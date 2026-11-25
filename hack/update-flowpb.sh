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

# Regenerates pkg/flowpb from the FlowStreamService protos at an Antrea revision. The
# proto package name is unchanged, so the gRPC full method name stays wire-identical to
# the Flow Aggregator's.
#
# Usage: hack/update-flowpb.sh [ANTREA_REF]
# ANTREA_REF defaults to the current pin, pkg/flowpb/UPSTREAM_SHA. Set ANTREA_DIR to a
# local antrea checkout to read the protos from git; ANTREA_REF may then be any ref,
# and is resolved to a commit before anything is read. Without ANTREA_DIR the protos
# are fetched from GitHub and ANTREA_REF must be a full 40-character commit SHA.
#
# The bindings are generated with the versions below, which the script requires. They
# are the ones Antrea itself uses for pkg/apis/flow/v1alpha1:
#   protoc 34.1 (recorded as v7.34.1 in the headers), from
#     https://github.com/protocolbuffers/protobuf/releases/tag/v34.1
#   go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
#   go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
# FLOWPB_ALLOW_TOOL_MISMATCH=1 downgrades a version mismatch to a warning; the generated
# headers then record the versions actually used. With the versions above the output
# can still differ from Antrea's own files in the formatting of doc comments, which
# may depend on the Go version that built the plugin, and in the go_package bytes of the
# embedded descriptor, which the rewrite below changes.
#
# Nothing in pkg/flowpb changes unless every step succeeds: the bindings are generated
# into a temporary directory and replace the checked-in ones, with UPSTREAM_SHA, last.
# Files that are not generated, such as policy.go, are left alone.
set -euo pipefail

PROTOC_VERSION="34.1"
PROTOC_GEN_GO_VERSION="v1.36.11"
PROTOC_GEN_GO_GRPC_VERSION="1.6.2"
GO_PACKAGE="antrea.io/antrea-ui/pkg/flowpb"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${ROOT}/pkg/flowpb"
PROTO_DIR="pkg/apis/flow/v1alpha1"
GENERATED=(flow.pb.go service.pb.go service_grpc.pb.go)

fail() {
  echo "update-flowpb: $*" >&2
  exit 1
}

# check_version NAME WANT GOT: a mismatch is fatal unless FLOWPB_ALLOW_TOOL_MISMATCH=1.
check_version() {
  [[ "$3" == "$2" ]] && return 0
  if [[ "${FLOWPB_ALLOW_TOOL_MISMATCH:-}" == "1" ]]; then
    echo "update-flowpb: warning: $1 is ${3:-unknown}, the checked-in bindings use $2" >&2
  else
    fail "$1 is ${3:-unknown}, need $2 (see the header of this script, or set FLOWPB_ALLOW_TOOL_MISMATCH=1)"
  fi
}

for tool in protoc protoc-gen-go protoc-gen-go-grpc; do
  command -v "${tool}" >/dev/null || fail "${tool} not found on PATH (see the header of this script for the supported versions)"
done
got="$(protoc --version 2>/dev/null)" || fail "protoc --version failed"
check_version protoc "${PROTOC_VERSION}" "${got#libprotoc }"
got="$(protoc-gen-go --version 2>/dev/null)" || fail "protoc-gen-go --version failed"
check_version protoc-gen-go "${PROTOC_GEN_GO_VERSION}" "${got##* }"
got="$(protoc-gen-go-grpc --version 2>/dev/null)" || fail "protoc-gen-go-grpc --version failed"
check_version protoc-gen-go-grpc "${PROTOC_GEN_GO_GRPC_VERSION}" "${got##* }"

REF="${1:-$(tr -d '[:space:]' < "${OUT}/UPSTREAM_SHA")}"
if [[ -n "${ANTREA_DIR:-}" ]]; then
  ANTREA_SHA="$(git -C "${ANTREA_DIR}" rev-parse --verify --quiet "${REF}^{commit}")" \
    || fail "${REF} is not a commit in ${ANTREA_DIR}"
else
  command -v curl >/dev/null || fail "curl not found on PATH (or set ANTREA_DIR)"
  [[ "${REF}" =~ ^[0-9a-f]{40}$ ]] \
    || fail "without ANTREA_DIR the ref must be a full 40-character commit SHA, got ${REF}"
  ANTREA_SHA="${REF}"
fi

TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"; rm -f "${OUT}"/.*.new' EXIT
SRC="${TMP}/src"
GEN="${TMP}/gen"
mkdir -p "${SRC}/${PROTO_DIR}" "${GEN}"
for f in flow.proto service.proto; do
  if [[ -n "${ANTREA_DIR:-}" ]]; then
    git -C "${ANTREA_DIR}" show "${ANTREA_SHA}:${PROTO_DIR}/${f}" > "${SRC}/${PROTO_DIR}/${f}"
  else
    curl -fsSL "https://raw.githubusercontent.com/antrea-io/antrea/${ANTREA_SHA}/${PROTO_DIR}/${f}" \
      -o "${SRC}/${PROTO_DIR}/${f}"
  fi
done

# Only the go_package changes: generated files land directly in pkg/flowpb.
sed -i.bak "s#option go_package = \"pkg/apis/flow/v1alpha1\";#option go_package = \"${GO_PACKAGE}\";#" \
  "${SRC}/${PROTO_DIR}"/*.proto
rm -f "${SRC}/${PROTO_DIR}"/*.bak

(cd "${SRC}" && protoc \
  --go_out="${GEN}" --go_opt=module="${GO_PACKAGE}" \
  --go-grpc_out="${GEN}" --go-grpc_opt=module="${GO_PACKAGE}" \
  "${PROTO_DIR}/flow.proto" "${PROTO_DIR}/service.proto") \
  || fail "protoc failed; pkg/flowpb is unchanged"
for f in "${GENERATED[@]}"; do
  [[ -s "${GEN}/${f}" ]] || fail "protoc did not produce ${f}; pkg/flowpb is unchanged"
done
echo "${ANTREA_SHA}" > "${GEN}/UPSTREAM_SHA"

# Stage next to the targets so that each replacement is a rename within one directory,
# then drop bindings that the protos no longer produce.
mkdir -p "${OUT}"
for f in "${GENERATED[@]}" UPSTREAM_SHA; do
  cp "${GEN}/${f}" "${OUT}/.${f}.new"
done
for f in "${GENERATED[@]}" UPSTREAM_SHA; do
  mv -f "${OUT}/.${f}.new" "${OUT}/${f}"
done
for f in "${OUT}"/*.pb.go; do
  [[ -e "${GEN}/$(basename "${f}")" ]] || rm -f "${f}"
done

echo "flowpb regenerated from antrea-io/antrea@${ANTREA_SHA}"
