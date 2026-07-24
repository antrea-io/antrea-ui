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

import type { AntreaPluginHost, AntreaPluginRegister } from '@antrea/ui-plugin-sdk';
import { PolicyManagementPage } from './policy-management-page.js';
import { PolicyDefinitionsPage } from './policy-definitions-page.js';
import { PolicyRecommendationsPage } from './policy-recommendations-page.js';

/**
 * Plugin entry point. The host shell imports this module and calls
 * register(host) with its AntreaPluginHost implementation, analogous to how
 * a Headlamp plugin's src/index.tsx calls registerRoute()/registerSidebarEntry().
 */
export const register: AntreaPluginRegister = (host: AntreaPluginHost) => {
  host.registerSidebarEntry({
    label: 'Policy Management',
    path: '/policies',
    icon: 'shield-check',
    children: [
      { label: 'Policy Definitions', path: '/policies/definitions' },
      { label: 'Policy Recommendations', path: '/policies/recommendations' },
    ],
  });

  host.registerRoute({
    path: 'policies',
    component: PolicyManagementPage,
    children: [
      { path: '', pathMatch: 'full', redirectTo: 'definitions' },
      { path: 'definitions', component: PolicyDefinitionsPage },
      { path: 'recommendations', component: PolicyRecommendationsPage },
    ],
  });
};

export { PolicyManagementPage } from './policy-management-page.js';
export { PolicyDefinitionsPage } from './policy-definitions-page.js';
export { PolicyRecommendationsPage } from './policy-recommendations-page.js';
export { PolicyService } from './policy.service.js';
export type { PolicyDefinition, PolicyType, PolicyListResult } from './policy.service.js';
