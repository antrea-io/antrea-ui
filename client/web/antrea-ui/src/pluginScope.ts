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

// The mounted page supplies session expiry handling. Namespace-aware pages also supply
// the router and last-scope callbacks when their element is attached.

import { createPluginNamespaceScope } from '@antrea/ui-components';
import type { NamespaceScopeHostAdapter } from '@antrea/ui-components';

export type PluginScopeNavigation = Pick<NamespaceScopeHostAdapter, 'writeScope' | 'rememberScope' | 'getResources'>;
type PluginScopeBindings = { navigation?: PluginScopeNavigation; sessionExpired(): void };

let bindings: PluginScopeBindings | null = null;

export function bindPluginScope(next: PluginScopeBindings | null): void {
    bindings = next;
}

const adapter: NamespaceScopeHostAdapter = {
    writeScope: namespace => bindings?.navigation?.writeScope(namespace),
    rememberScope: scope => bindings?.navigation?.rememberScope(scope),
    getResources: () => bindings?.navigation?.getResources?.(),
    sessionExpired: () => bindings?.sessionExpired(),
};

export const pluginNamespaceScope = createPluginNamespaceScope(adapter);
