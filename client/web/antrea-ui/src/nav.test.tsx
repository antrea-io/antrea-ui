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

import { render } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import NavTab from './nav';
import type { PluginSidebarEntry } from './plugins';
import type { AccessSummary } from '@antrea/ui-components';
import { useAccess } from './access';

vi.mock('./access', () => ({ useAccess: vi.fn() }));
const mockUseAccess = vi.mocked(useAccess);

const podCounterEntry: PluginSidebarEntry = { label: 'Pod Counter', path: '/plugin/pod-counter' };

function summaryAllowing(rules: Partial<AccessSummary['rules']> = {}): AccessSummary {
    return {
        username: 'alice',
        groups: [],
        clusterAdmin: false,
        rules: { resourceRules: [], nonResourceRules: [], incomplete: false, ...rules },
        namespaces: [],
    };
}

describe('NavTab', () => {
    beforeEach(() => {
        mockUseAccess.mockReturnValue({ summary: null, loaded: true });
    });

    test('no plugin sidebar entries renders no extra items', () => {
        render(<NavTab pluginSidebarEntries={[]} />, { wrapper: MemoryRouter });

        expect(document.querySelector('a[href="/plugin/pod-counter"]')).toBeNull();
    });

    test('a plugin sidebar entry links to its path', () => {
        render(<NavTab pluginSidebarEntries={[podCounterEntry]} />, { wrapper: MemoryRouter });

        const link = document.querySelector('a[href="/plugin/pod-counter"]');
        expect(link).not.toBeNull();
        expect(link!.textContent).toContain('Pod Counter');
    });

    test('the plugin nav item is marked active when the current path matches', () => {
        render(
            <MemoryRouter initialEntries={['/plugin/pod-counter']}>
                <NavTab pluginSidebarEntries={[podCounterEntry]} />
            </MemoryRouter>
        );

        const link = document.querySelector('a[href="/plugin/pod-counter"]');
        // React assigns non-standard boolean props on custom (hyphenated) elements as a DOM
        // property, not a reflected attribute, so this reads the property rather than
        // hasAttribute('active').
        const navItem = link!.closest('antrea-nav-item') as unknown as { active?: boolean };
        expect(navItem.active).toBe(true);
    });

    test('the plugin nav item is not active on an unrelated path', () => {
        render(
            <MemoryRouter initialEntries={['/summary']}>
                <NavTab pluginSidebarEntries={[podCounterEntry]} />
            </MemoryRouter>
        );

        const link = document.querySelector('a[href="/plugin/pod-counter"]');
        const navItem = link!.closest('antrea-nav-item') as unknown as { active?: boolean };
        expect(navItem.active).toBeFalsy();
    });
});

describe('NavTab — permission gating', () => {
    test('while unloaded, renders no core items', () => {
        mockUseAccess.mockReturnValue({ summary: null, loaded: false });
        render(<NavTab pluginSidebarEntries={[]} />, { wrapper: MemoryRouter });

        expect(document.querySelector('a[href="/overview"]')).toBeNull();
        expect(document.querySelector('a[href="/summary"]')).toBeNull();
        expect(document.querySelector('a[href="/traceflow"]')).toBeNull();
        // Flow Visibility and Settings have no per-user RBAC, so they are not gated on load.
        expect(document.querySelector('a[href="/flows"]')).not.toBeNull();
        expect(document.querySelector('a[href="/settings"]')).not.toBeNull();
    });

    test('a null summary (fetch failed) fails open: all core tabs show', () => {
        mockUseAccess.mockReturnValue({ summary: null, loaded: true });
        render(<NavTab pluginSidebarEntries={[]} />, { wrapper: MemoryRouter });

        expect(document.querySelector('a[href="/overview"]')).not.toBeNull();
        expect(document.querySelector('a[href="/summary"]')).not.toBeNull();
        expect(document.querySelector('a[href="/traceflow"]')).not.toBeNull();
    });

    test('Traceflow is hidden without create permission, Summary still shows', () => {
        mockUseAccess.mockReturnValue({
            summary: summaryAllowing({
                resourceRules: [{ apiGroups: ['crd.antrea.io'], resources: ['antreaagentinfos'], verbs: ['list'] }],
            }),
            loaded: true,
        });
        render(<NavTab pluginSidebarEntries={[]} />, { wrapper: MemoryRouter });

        expect(document.querySelector('a[href="/summary"]')).not.toBeNull();
        expect(document.querySelector('a[href="/traceflow"]')).toBeNull();
    });

    test('Summary is hidden when none of its three gates is granted', () => {
        mockUseAccess.mockReturnValue({ summary: summaryAllowing(), loaded: true });
        render(<NavTab pluginSidebarEntries={[]} />, { wrapper: MemoryRouter });

        expect(document.querySelector('a[href="/summary"]')).toBeNull();
    });

    test('Overview shows when the user can list Pods, even without Summary permissions', () => {
        mockUseAccess.mockReturnValue({
            summary: summaryAllowing({
                resourceRules: [{ apiGroups: [''], resources: ['pods'], verbs: ['list'] }],
            }),
            loaded: true,
        });
        render(<NavTab pluginSidebarEntries={[]} />, { wrapper: MemoryRouter });

        expect(document.querySelector('a[href="/overview"]')).not.toBeNull();
        expect(document.querySelector('a[href="/summary"]')).toBeNull();
    });

    test('Overview is hidden when none of its inventory gates is granted', () => {
        mockUseAccess.mockReturnValue({ summary: summaryAllowing(), loaded: true });
        render(<NavTab pluginSidebarEntries={[]} />, { wrapper: MemoryRouter });

        expect(document.querySelector('a[href="/overview"]')).toBeNull();
    });
});
