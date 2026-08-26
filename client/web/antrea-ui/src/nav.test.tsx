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

describe('NavTab — Flow Visibility built-in nesting', () => {
    beforeEach(() => {
        mockUseAccess.mockReturnValue({ summary: null, loaded: true });
    });

    test('Flow List and Service Map render nested under Flow Visibility, unconditionally', () => {
        render(<NavTab pluginSidebarEntries={[]} />, { wrapper: MemoryRouter });

        // Both the Flow Visibility header itself and the nested Flow List item link to
        // /flows/list — the header IS the default sub-page's link, not a separate destination.
        const listLinks = document.querySelectorAll('a[href="/flows/list"]');
        const mapLink = document.querySelector('a[href="/flows/map"]');
        expect(listLinks).toHaveLength(2);
        expect(mapLink).not.toBeNull();

        const group = mapLink!.closest('antrea-nav-group');
        expect(group).not.toBeNull();
        expect(listLinks[0].closest('antrea-nav-group')).toBe(group);
        expect(listLinks[1].closest('antrea-nav-group')).toBe(group);
    });

    test('Flow Visibility group auto-expands when on the Service Map sub-page', () => {
        render(
            <MemoryRouter initialEntries={['/flows/map']}>
                <NavTab pluginSidebarEntries={[]} />
            </MemoryRouter>
        );

        const mapLink = document.querySelector('a[href="/flows/map"]');
        const group = mapLink!.closest('antrea-nav-group') as unknown as { hasActiveChild?: boolean };
        expect(group.hasActiveChild).toBe(true);

        const mapNavItem = mapLink!.closest('antrea-nav-item') as unknown as { active?: boolean };
        expect(mapNavItem.active).toBe(true);
    });
});

describe('NavTab — nested plugin entries', () => {
    beforeEach(() => {
        mockUseAccess.mockReturnValue({ summary: null, loaded: true });
    });

    test('a plugin entry nested under another plugin entry renders inside an antrea-nav-group', () => {
        const parentEntry: PluginSidebarEntry = { label: 'Parent', path: '/plugin/parent' };
        const childEntry: PluginSidebarEntry = { label: 'Child', path: '/plugin/child', parentPath: 'plugin/parent' };
        render(<NavTab pluginSidebarEntries={[parentEntry, childEntry]} />, { wrapper: MemoryRouter });

        const parentLink = document.querySelector('a[href="/plugin/parent"]');
        const childLink = document.querySelector('a[href="/plugin/child"]');
        expect(parentLink).not.toBeNull();
        expect(childLink).not.toBeNull();

        const group = childLink!.closest('antrea-nav-group');
        expect(group).not.toBeNull();
        expect(parentLink!.closest('antrea-nav-group')).toBe(group);
    });

    test('a plugin entry nested under a built-in page renders inside an antrea-nav-group', () => {
        const childEntry: PluginSidebarEntry = { label: 'Extra Flows Page', path: '/plugin/extra-flows', parentPath: 'flows' };
        render(<NavTab pluginSidebarEntries={[childEntry]} />, { wrapper: MemoryRouter });

        const flowsLink = document.querySelector('a[href="/flows/list"]');
        const childLink = document.querySelector('a[href="/plugin/extra-flows"]');
        expect(childLink!.closest('antrea-nav-group')).toBe(flowsLink!.closest('antrea-nav-group'));
    });

    test('the group auto-expands (hasActiveChild) when the current path matches a nested entry', () => {
        const childEntry: PluginSidebarEntry = { label: 'Extra Flows Page', path: '/plugin/extra-flows', parentPath: 'flows' };
        render(
            <MemoryRouter initialEntries={['/plugin/extra-flows']}>
                <NavTab pluginSidebarEntries={[childEntry]} />
            </MemoryRouter>
        );

        const childLink = document.querySelector('a[href="/plugin/extra-flows"]');
        const group = childLink!.closest('antrea-nav-group') as unknown as { hasActiveChild?: boolean };
        expect(group.hasActiveChild).toBe(true);
    });

    test('an entry with an unresolvable parentPath is dropped from the sidebar entirely, matching getPluginSidebarEntries', () => {
        // NavTab trusts its pluginSidebarEntries prop as already resolved (see plugins.ts's
        // resolveParentPaths) — an entry a caller hands it directly with a dangling parentPath
        // has no group to nest under, so it renders nothing rather than falling back to top-level.
        const orphanEntry: PluginSidebarEntry = { label: 'Orphan', path: '/plugin/orphan', parentPath: 'no-such-parent' };
        render(<NavTab pluginSidebarEntries={[orphanEntry]} />, { wrapper: MemoryRouter });

        expect(document.querySelector('a[href="/plugin/orphan"]')).toBeNull();
    });
});

describe('NavTab — permission gating', () => {
    test('while unloaded, renders no core items', () => {
        mockUseAccess.mockReturnValue({ summary: null, loaded: false });
        render(<NavTab pluginSidebarEntries={[]} />, { wrapper: MemoryRouter });

        expect(document.querySelector('a[href="/summary"]')).toBeNull();
        expect(document.querySelector('a[href="/traceflow"]')).toBeNull();
        // Flow Visibility and Settings have no per-user RBAC, so they are not gated on load.
        expect(document.querySelector('a[href="/flows/list"]')).not.toBeNull();
        expect(document.querySelector('a[href="/settings"]')).not.toBeNull();
    });

    test('a null summary (fetch failed) fails open: both core tabs show', () => {
        mockUseAccess.mockReturnValue({ summary: null, loaded: true });
        render(<NavTab pluginSidebarEntries={[]} />, { wrapper: MemoryRouter });

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
});
