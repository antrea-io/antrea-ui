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

# Build antrea-ui-components first: antrea-ui now consumes its published dist/ output
# (package.json's main/module/types point there), not its raw TypeScript source, so it
# needs its devDependencies (vite, typescript) too, not just its runtime ones.
WORKDIR /antrea-ui-components
COPY client/web/antrea-ui-components/package.json client/web/antrea-ui-components/package-lock.json ./
COPY client/web/antrea-ui-components/src ./src
COPY client/web/antrea-ui-components/tsconfig.json client/web/antrea-ui-components/vite.config.ts ./
RUN if [ -n "$NPM_REGISTRY" ]; then npm config set registry "$NPM_REGISTRY"; fi && \
    npm install && \
    npm run build

WORKDIR /app

COPY client/web/antrea-ui/package.json .
COPY client/web/antrea-ui/yarn.lock .
COPY client/web/antrea-ui/.yarnrc.yml .
COPY client/web/antrea-ui/.yarn ./.yarn

RUN if [ -n "$NPM_REGISTRY" ]; then \
      echo "npmRegistryServer: \"$NPM_REGISTRY\"" >> .yarnrc.yml; \
    fi

# Corepack fetches the yarn binary itself from repo.yarnpkg.com, ignoring NPM_REGISTRY —
# on networks that block it (TLS "unable to get local issuer certificate"), route it
# through the same registry too (trailing slash stripped to avoid a "//" 404).
RUN export COREPACK_NPM_REGISTRY="${NPM_REGISTRY%/}" && \
    corepack enable && yarn install --immutable

COPY client/web/antrea-ui .
ARG NODE_ENV=production
RUN yarn build

FROM nginxinc/nginx-unprivileged:1.29

COPY --from=build-web /app/build /app
COPY build/scripts/nginx-reloader.sh /docker-entrypoint.d/99-nginx-reloader.sh
