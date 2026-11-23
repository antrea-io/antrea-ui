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


// The SDK is types-only on the plugin side but its functions run against window.__antreaPluginHost;
// these check them against hosts with and without the Namespace scope API.

import { afterEach, describe, expect, test } from 'vitest';
import { namespaceScope, namespaceScopeSupported } from '@antrea/ui-plugin-sdk';
import type { AntreaPluginHost } from '@antrea/ui-plugin-sdk';
import '@antrea/ui-components';
import './plugins';

const original = window.__antreaPluginHost;
afterEach(() => { window.__antreaPluginHost = original; });

function oldHost(): AntreaPluginHost {
    const host: AntreaPluginHost = { ...original! };
    delete host.namespaceScope;
    return host;
}

describe('namespaceScope', () => {
    test('is provided by this host', () => {
        expect(namespaceScopeSupported()).toBe(true);
        expect(namespaceScope().search({ kind: 'namespace', namespace: 'ns-a' })).toBe('?namespace=ns-a');
        expect(namespaceScope().search({ kind: 'all' })).toBe('?namespace=_all');
        expect(namespaceScope().search(null)).toBe('');
    });

    test('throws on a host without it, and is not supported', () => {
        window.__antreaPluginHost = oldHost();
        expect(() => namespaceScope()).toThrow(/does not provide namespaceScope/);
        expect(namespaceScopeSupported()).toBe(false);
    });

    test.each(['__cluster_wide__', '_all'])('rejects an invalid Namespace name %s in a link', namespace => {
        expect(() => namespaceScope().search({ kind: 'namespace', namespace })).toThrow(/not a valid namespace name/);
    });
});
