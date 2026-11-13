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

# Build from the repository root:
#   docker build -t antrea/antrea-ui-fakefa -f test/benchmark/flowvisibility/fakefa.dockerfile \
#     --build-arg GO_VERSION=$(head -n 1 .go-version) .

ARG GO_VERSION

FROM golang:${GO_VERSION} as build-go
WORKDIR /app

COPY go.mod .
COPY go.sum .
RUN go mod download

COPY apis ./apis
COPY pkg ./pkg
COPY test/benchmark/flowvisibility ./test/benchmark/flowvisibility

RUN CGO_ENABLED=0 go build -o /app/fakefa ./test/benchmark/flowvisibility/cmd/fakefa

FROM gcr.io/distroless/static:nonroot

USER 65532:65532

COPY --from=build-go /app/fakefa /app/fakefa

ENTRYPOINT [ "/app/fakefa" ]
