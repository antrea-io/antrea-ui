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

# Downstream build of antrea-ui with an Angular + Clarity Design System shell,
# reusing the same Lit web components (antrea-ui-components) as the default
# React frontend. See client/web/antrea-ui-angular/README.md.

FROM node:24-bullseye-slim as build-web

# Optional: override the npm registry (e.g., for corporate proxies).
# Leave unset to use the default registry (npmjs.org).
ARG NPM_REGISTRY=""

# Build antrea-ui-components to a plain JS bundle first. Unlike the React
# frontend (which compiles the library's TypeScript source directly via
# Vite), Angular's stricter TS program would otherwise choke on source
# written for the library's own toolchain (missing 'override' modifiers,
# tslib decorator helpers, etc).
WORKDIR /antrea-ui-components
COPY client/web/antrea-ui-components/package.json client/web/antrea-ui-components/package-lock.json ./
COPY client/web/antrea-ui-components/src ./src
COPY client/web/antrea-ui-components/tsconfig.json client/web/antrea-ui-components/vite.config.ts ./
RUN if [ -n "$NPM_REGISTRY" ]; then npm config set registry "$NPM_REGISTRY"; fi && \
    npm install && \
    npm run build

# Plugin packages, consumed by antrea-ui-angular via "link:../<name>" (a yarn
# portal to a sibling directory) — see client/web/antrea-ui-plugin-*/README.md.
# They must sit next to /app (i.e. at /) for that relative symlink to resolve.
# Their TypeScript source is compiled directly by antrea-ui-angular's own
# Angular CLI build (via tsconfig.app.json's "include"), but the Angular
# compiler still resolves each file's own bare imports (@angular/core, tslib,
# @antrea/ui-plugin-sdk) starting from that file's closest node_modules — so
# each plugin package needs its own `npm install` here, pinned to the exact
# same @angular/* version as antrea-ui-angular (see each package's README).
WORKDIR /antrea-ui-plugin-sdk
COPY client/web/antrea-ui-plugin-sdk/package.json client/web/antrea-ui-plugin-sdk/package-lock.json client/web/antrea-ui-plugin-sdk/tsconfig.json ./
COPY client/web/antrea-ui-plugin-sdk/src ./src
RUN if [ -n "$NPM_REGISTRY" ]; then npm config set registry "$NPM_REGISTRY"; fi && \
    npm install

WORKDIR /antrea-ui-plugin-policy-management
COPY client/web/antrea-ui-plugin-policy-management/package.json client/web/antrea-ui-plugin-policy-management/package-lock.json client/web/antrea-ui-plugin-policy-management/tsconfig.json ./
COPY client/web/antrea-ui-plugin-policy-management/src ./src
RUN if [ -n "$NPM_REGISTRY" ]; then npm config set registry "$NPM_REGISTRY"; fi && \
    npm install

WORKDIR /app

COPY client/web/antrea-ui-angular/package.json .
COPY client/web/antrea-ui-angular/yarn.lock .
COPY client/web/antrea-ui-angular/.yarnrc.yml .
COPY client/web/antrea-ui-angular/.yarn ./.yarn

RUN if [ -n "$NPM_REGISTRY" ]; then \
      echo "npmRegistryServer: \"$NPM_REGISTRY\"" >> .yarnrc.yml; \
    fi

RUN corepack enable && yarn install --immutable

COPY client/web/antrea-ui-angular .
ARG NODE_ENV=production
RUN yarn build

FROM nginxinc/nginx-unprivileged:1.29

COPY --from=build-web /app/dist/antrea-ui-angular/browser /app
COPY build/scripts/nginx-reloader.sh /docker-entrypoint.d/99-nginx-reloader.sh
