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

# CI-only: proves out the "build a plugin tarball, ship it inside the frontend image" workflow
# without touching the mainline build/frontend.dockerfile. Not used in any production build.
#
# Usage (see .github/workflows/plugin-ci.yml):
#   docker build -f build/frontend.dockerfile -t antrea-ui-frontend:ci .
#   docker build -f build/ci/frontend-with-example-plugin.dockerfile \
#     --build-arg BASE_IMAGE=antrea-ui-frontend:ci -t antrea-ui-frontend:ci-with-plugin .

ARG BASE_IMAGE=antrea-ui-frontend:ci
FROM ${BASE_IMAGE}

USER root
COPY plugins/examples/pod-counter/pod-counter.tgz /tmp/pod-counter.tgz
RUN mkdir -p /etc/plugins/pod-counter \
    && tar xzf /tmp/pod-counter.tgz -C /etc/plugins/pod-counter \
    && chmod -R a+rX /etc/plugins/pod-counter \
    && rm /tmp/pod-counter.tgz
USER 101
