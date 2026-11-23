// Copyright 2026 Antrea Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import { createNamespaceScope } from './namespace-scope-controller.js';
import type { NamespaceScopeHandle, NamespaceScopeHostAdapter, NamespaceScopeOptions } from './namespace-scope-controller.js';
import { searchWithScope, scopeToValue } from './namespace-scope.js';
import type { NamespaceScope } from './namespace-scope.js';
import { isValidNamespaceName } from './namespace-access.js';

/** The Namespace scope API a host exposes to plugin code as window.__antreaPluginHost.namespaceScope.
 * It runs the host's copy of the scope rules, so that a plugin keeps no Namespace list of its
 * own (a plugin bundle has its own module state, which sign-out would not reach). */
export interface PluginNamespaceScope {
    create(options: NamespaceScopeOptions): NamespaceScopeHandle;
    /** The query string that carries `scope` to another Namespace-aware page, for links: '' for
     * none or All Namespaces' `?namespace=_all`. Throws for an invalid Namespace name. */
    search(scope: NamespaceScope | null): string;
}

/** Creates a host's PluginNamespaceScope. `adapter` is how the host is told what the page chose
 * and where the URL goes (a router that cannot be reached through history.replaceState). Without
 * one, handles dispatch the scope events from their `target`, which a host that listens for them
 * on the plugin element (the React host's) already handles. */
export function createPluginNamespaceScope(adapter?: NamespaceScopeHostAdapter): PluginNamespaceScope {
    return {
        create: ({ target, resources }) => createNamespaceScope({ target, resources, adapter }),
        search: scope => {
            if (scope?.kind === 'namespace' && !isValidNamespaceName(scope.namespace)) {
                throw new Error(`"${scope.namespace}" is not a valid namespace name.`);
            }
            return searchWithScope('', scopeToValue(scope));
        },
    };
}
