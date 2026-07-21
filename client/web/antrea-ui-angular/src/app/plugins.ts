/**
 * Copyright 2026 Antrea Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import type { Route } from '@angular/router';
import type { AntreaPluginHost, PluginSidebarEntry } from '@antrea/ui-plugin-sdk';
import { register as registerPolicyManagementPlugin } from '@antrea/ui-plugin-policy-management';

// Plugins are statically linked npm packages today (see each package's
// README for the planned dynamic-loading follow-up), but they never import
// this app's internals directly — they only see the AntreaPluginHost object
// built here, so swapping in a runtime loader later won't require touching
// plugin code.
const pluginRouteRegistry: Route[] = [];
const pluginSidebarEntryRegistry: PluginSidebarEntry[] = [];
let authTokenAccessor: () => string | undefined = () => undefined;

const host: AntreaPluginHost = {
  registerRoute: (route) => pluginRouteRegistry.push(route),
  registerSidebarEntry: (entry) => pluginSidebarEntryRegistry.push(entry),
  getAuthToken: () => authTokenAccessor(),
};

registerPolicyManagementPlugin(host);

/** Called once the app injector exists, so getAuthToken() can reach the real AuthService. */
export function setPluginAuthTokenAccessor(fn: () => string | undefined): void {
  authTokenAccessor = fn;
}

export const pluginRoutes: Route[] = pluginRouteRegistry;
export const pluginSidebarEntries: PluginSidebarEntry[] = pluginSidebarEntryRegistry;

/** Provided to ANTREA_PLUGIN_HOST so plugins' injected services (e.g. PolicyService) can reach it. */
export const pluginHost: AntreaPluginHost = host;
