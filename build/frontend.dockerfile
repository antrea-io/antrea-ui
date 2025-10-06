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

FROM node:22-bullseye-slim as build-web

WORKDIR /app

COPY client/web/antrea-ui/package.json .
COPY client/web/antrea-ui/yarn.lock .
COPY client/web/antrea-ui/.yarnrc.yml .
COPY client/web/antrea-ui/.yarn ./.yarn

RUN corepack enable && yarn install --immutable

COPY client/web/antrea-ui .
ARG NODE_ENV=production
RUN yarn build

FROM nginxinc/nginx-unprivileged:1.27

COPY --from=build-web /app/build /app
COPY build/scripts/nginx-reloader.sh /docker-entrypoint.d/99-nginx-reloader.sh
