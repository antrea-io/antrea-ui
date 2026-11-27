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

# Offline regression tests for hack/update-flowpb.sh. Each case runs a copy of the script
# in a disposable tree against a disposable fake antrea repository. protoc, its plugins
# and curl are stubs on a restricted PATH, so the tests need neither the real toolchain
# nor the network.
#
# Usage: hack/test/update-flowpb-test.sh   (also run by make test)
# The test_* functions are found by name at the end, so shellcheck cannot see them called.
# shellcheck disable=SC2317
set -euo pipefail

HACK="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BASH_BIN="$(command -v bash)"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com
unset ANTREA_DIR FLOWPB_ALLOW_TOOL_MISMATCH STUB_MODE STUB_GO_VERSION

# The only tools the script under test can see, besides the stubs added per case.
BASE_BIN="${WORK}/bin/base"
mkdir -p "${BASE_BIN}"
for tool in basename bash cat cp dirname env git mkdir mktemp mv rm sed sh tr; do
  ln -s "$(command -v "${tool}")" "${BASE_BIN}/${tool}"
done

# protoc stub: writes one binding per proto and output flag, holding the proto it was
# generated from. STUB_MODE=fail writes part of the output and fails, as a compiler error
# or a crashing plugin would; STUB_MODE=incomplete succeeds without the gRPC binding.
PROTOC_BIN="${WORK}/bin/protoc"
mkdir -p "${PROTOC_BIN}"
cat > "${PROTOC_BIN}/protoc" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "--version" ]]; then echo "libprotoc 34.1"; exit 0; fi
go_out="" grpc_out="" protos=()
for arg in "$@"; do
  case "${arg}" in
    --go_out=*) go_out="${arg#--go_out=}" ;;
    --go-grpc_out=*) grpc_out="${arg#--go-grpc_out=}" ;;
    --*) ;;
    *) protos+=("${arg}") ;;
  esac
done
for proto in "${protos[@]}"; do
  base="$(basename "${proto}" .proto)"
  { echo "// stub protoc-gen-go"; cat "${proto}"; } > "${go_out}/${base}.pb.go"
  if [[ "${STUB_MODE:-}" == "fail" ]]; then echo "${proto}: syntax error" >&2; exit 1; fi
done
if [[ "${STUB_MODE:-}" != "incomplete" ]]; then
  { echo "// stub protoc-gen-go-grpc"; cat "${protos[1]}"; } > "${grpc_out}/service_grpc.pb.go"
fi
EOF
PLUGIN_BIN="${WORK}/bin/plugins"
mkdir -p "${PLUGIN_BIN}"
cat > "${PLUGIN_BIN}/protoc-gen-go" <<'EOF'
#!/usr/bin/env bash
echo "protoc-gen-go ${STUB_GO_VERSION:-v1.36.11}"
EOF
cat > "${PLUGIN_BIN}/protoc-gen-go-grpc" <<'EOF'
#!/usr/bin/env bash
echo "protoc-gen-go-grpc 1.6.2"
EOF
cat > "${PLUGIN_BIN}/curl" <<'EOF'
#!/usr/bin/env bash
echo "curl: the tests run without network" >&2
exit 7
EOF
chmod +x "${PROTOC_BIN}"/* "${PLUGIN_BIN}"/*
FULL_PATH="${PROTOC_BIN}:${PLUGIN_BIN}:${BASE_BIN}"

# Fake antrea history: A is the original pin, B changes service.proto, and main points
# at B.
ANTREA="${WORK}/antrea"
PROTO_DIR="pkg/apis/flow/v1alpha1"
put() { mkdir -p "$(dirname "${ANTREA}/$1")"; printf '%s\n' "$2" > "${ANTREA}/$1"; }
commit() { git -C "${ANTREA}" add -A && git -C "${ANTREA}" commit -q -m "$1" && git -C "${ANTREA}" rev-parse HEAD; }
git init -q -b main "${ANTREA}"
put "${PROTO_DIR}/flow.proto" 'option go_package = "pkg/apis/flow/v1alpha1";
message Flow {}'
put "${PROTO_DIR}/service.proto" 'option go_package = "pkg/apis/flow/v1alpha1";
service FlowStreamService {}'
SHA_A="$(commit A)"
put "${PROTO_DIR}/service.proto" 'option go_package = "pkg/apis/flow/v1alpha1";
service FlowStreamService { rpc StreamFlows() }'
SHA_B="$(commit B)"

# new_tree: a disposable tree with the script, the checked-in bindings and a hand-written
# file, pinned at A.
new_tree() {
  TREE="$(mktemp -d "${WORK}/tree.XXXXXX")"
  mkdir -p "${TREE}/hack" "${TREE}/pkg/flowpb"
  cp "${HACK}/update-flowpb.sh" "${TREE}/hack/"
  for f in flow.pb.go service.pb.go service_grpc.pb.go; do
    echo "// checked-in ${f}" > "${TREE}/pkg/flowpb/${f}"
  done
  echo "// hand-written" > "${TREE}/pkg/flowpb/policy.go"
  echo "${SHA_A}" > "${TREE}/pkg/flowpb/UPSTREAM_SHA"
}

# snapshot DIR: every file in DIR, hidden ones included, with its content.
snapshot() {
  local f
  for f in "$1"/* "$1"/.[!.]*; do
    [[ -e "${f}" ]] || continue
    echo "== ${f##*/}"
    cat "${f}"
  done
}

# run PATH SCRIPT ARGS...: runs a script of the current tree; sets STATUS and OUTPUT.
run() {
  local path="$1" script="$2"
  shift 2
  STATUS=0
  OUTPUT="$(PATH="${path}" "${BASH_BIN}" "${TREE}/hack/${script}" "$@" 2>&1)" || STATUS=$?
}

die() { echo "    $*"; echo "    output: ${OUTPUT:-}" | sed 's/^/    /'; exit 1; }
expect_status() { [[ "${STATUS}" == "$1" ]] || die "exit status ${STATUS}, want $1"; }
expect_failure() { [[ "${STATUS}" != 0 ]] || die "exit status 0, want a failure"; }
expect_output() { [[ "${OUTPUT}" == *"$1"* ]] || die "output lacks: $1"; }

# expect_regen_fails PATH ARGS...: update-flowpb.sh fails and pkg/flowpb is byte-identical.
expect_regen_fails() {
  local before
  before="$(snapshot "${TREE}/pkg/flowpb")"
  run "$@"
  expect_failure
  [[ "$(snapshot "${TREE}/pkg/flowpb")" == "${before}" ]] || die "pkg/flowpb changed"
}

test_protoc_missing() {
  new_tree
  expect_regen_fails "${PLUGIN_BIN}:${BASE_BIN}" update-flowpb.sh
  expect_output "protoc not found on PATH"
}

test_plugin_version_mismatch() {
  new_tree
  STUB_GO_VERSION=v1.30.0 expect_regen_fails "${FULL_PATH}" update-flowpb.sh
  expect_output "protoc-gen-go is v1.30.0, need v1.36.11"
}

test_compiler_error_after_partial_output() {
  new_tree
  STUB_MODE=fail ANTREA_DIR="${ANTREA}" expect_regen_fails "${FULL_PATH}" update-flowpb.sh main
  expect_output "syntax error"
  expect_output "protoc failed; pkg/flowpb is unchanged"
}

test_incomplete_output() {
  new_tree
  STUB_MODE=incomplete ANTREA_DIR="${ANTREA}" expect_regen_fails "${FULL_PATH}" update-flowpb.sh main
  expect_output "protoc did not produce service_grpc.pb.go"
}

test_unknown_ref() {
  new_tree
  ANTREA_DIR="${ANTREA}" expect_regen_fails "${FULL_PATH}" update-flowpb.sh no-such-branch
  expect_output "no-such-branch is not a commit"
}

test_abbreviated_sha_without_antrea_dir() {
  new_tree
  expect_regen_fails "${FULL_PATH}" update-flowpb.sh "${SHA_B:0:12}"
  expect_output "must be a full 40-character commit SHA"
}

test_symbolic_ref_resolves_to_commit() {
  new_tree
  ANTREA_DIR="${ANTREA}" run "${FULL_PATH}" update-flowpb.sh main
  expect_status 0
  [[ "$(cat "${TREE}/pkg/flowpb/UPSTREAM_SHA")" == "${SHA_B}" ]] || die "pin is not ${SHA_B}"
  [[ "$(cat "${TREE}/pkg/flowpb/service_grpc.pb.go")" == *'go_package = "antrea.io/antrea-ui/pkg/flowpb"'*'rpc StreamFlows'* ]] \
    || die "bindings were not generated from the rewritten protos at B"
  [[ "$(cat "${TREE}/pkg/flowpb/policy.go")" == "// hand-written" ]] || die "hand-written file changed"
  [[ -z "$(snapshot "${TREE}/pkg/flowpb" | sed -n '/^== \./p')" ]] || die "staging files left behind"
}

failed=0
for t in $(declare -F | sed -n 's/^declare -f \(test_.*\)/\1/p'); do
  # Not in a conditional, so that errexit stays in force inside the case.
  set +e
  (set -e; "${t}")
  status=$?
  set -e
  if [[ ${status} -eq 0 ]]; then
    echo "ok   ${t}"
  else
    echo "FAIL ${t}"
    failed=1
  fi
done
exit "${failed}"
