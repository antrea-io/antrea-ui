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

# Built from the repository root by ci/e2e-supportbundle.sh.

ARG GO_VERSION

FROM golang:${GO_VERSION} AS build-go
WORKDIR /app

COPY go.mod .
COPY go.sum .
RUN go mod download

COPY apis ./apis
COPY test/e2e/supportbundle-source ./test/e2e/supportbundle-source

RUN CGO_ENABLED=0 go build -o /supportbundle-source ./test/e2e/supportbundle-source

FROM gcr.io/distroless/static:nonroot

USER 65532:65532

COPY --from=build-go /supportbundle-source /supportbundle-source

ENTRYPOINT [ "/supportbundle-source" ]
