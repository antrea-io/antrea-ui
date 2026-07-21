# Copyright 2023 Antrea Authors.
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

FROM node:24-bullseye-slim as build-web

# Optional: override the npm registry (e.g., for corporate proxies).
# Leave unset to use the default registry (npmjs.org).
ARG NPM_REGISTRY=""

# antrea-ui and antrea-ui-components share one Yarn workspace (client/web/), so they share
# one install. Copy just the manifests first so this layer caches independently of source edits.
WORKDIR /app
COPY client/web/package.json client/web/yarn.lock client/web/.yarnrc.yml ./
COPY client/web/.yarn ./.yarn
COPY client/web/antrea-ui/package.json ./antrea-ui/package.json
COPY client/web/antrea-ui-components/package.json ./antrea-ui-components/package.json

RUN if [ -n "$NPM_REGISTRY" ]; then \
      echo "npmRegistryServer: \"$NPM_REGISTRY\"" >> .yarnrc.yml; \
    fi

# Corepack fetches the yarn binary itself from repo.yarnpkg.com, ignoring NPM_REGISTRY —
# on networks that block it (TLS "unable to get local issuer certificate"), route it
# through the same registry too (trailing slash stripped to avoid a "//" 404).
RUN export COREPACK_NPM_REGISTRY="${NPM_REGISTRY%/}" && \
    corepack enable && yarn install --immutable

# Build antrea-ui-components first: antrea-ui consumes its published dist/ output (see the
# Vite/Vitest source alias in antrea-ui for why that's dev-only), not its raw TypeScript source.
COPY client/web/antrea-ui-components/src ./antrea-ui-components/src
COPY client/web/antrea-ui-components/tsconfig.json client/web/antrea-ui-components/vite.config.ts ./antrea-ui-components/
RUN yarn workspace @antrea/ui-components run build

COPY client/web/antrea-ui ./antrea-ui
ARG NODE_ENV=production
RUN yarn workspace antrea-ui run build

FROM nginxinc/nginx-unprivileged:1.29

COPY --from=build-web /app/antrea-ui/build /app
COPY build/scripts/nginx-reloader.sh /docker-entrypoint.d/99-nginx-reloader.sh
