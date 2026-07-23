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
import type { PluginManifest } from './plugins';

const podCounterPlugin: PluginManifest = {
    name: 'pod-counter',
    version: '0.1.0',
    entry: 'index.js',
    tag: 'antrea-plugin-pod-counter',
    navItem: { label: 'Pod Counter', path: '/plugin/pod-counter' },
};

describe('NavTab', () => {
    test('plugins with no navItem are not rendered', () => {
        const noNavItem: PluginManifest = { name: 'headless', version: '0.1.0', entry: 'index.js', tag: 'antrea-plugin-headless' };
        render(<NavTab plugins={[noNavItem]} />, { wrapper: MemoryRouter });

        expect(document.querySelector('a[href="/headless"]')).toBeNull();
    });

    test('a plugin with a navItem gets a sidebar entry linking to its path', () => {
        render(<NavTab plugins={[podCounterPlugin]} />, { wrapper: MemoryRouter });

        const link = document.querySelector('a[href="/plugin/pod-counter"]');
        expect(link).not.toBeNull();
        expect(link!.textContent).toContain('Pod Counter');
    });

    test('the plugin nav item is marked active when the current path matches', () => {
        render(
            <MemoryRouter initialEntries={['/plugin/pod-counter']}>
                <NavTab plugins={[podCounterPlugin]} />
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
                <NavTab plugins={[podCounterPlugin]} />
            </MemoryRouter>
        );

        const link = document.querySelector('a[href="/plugin/pod-counter"]');
        const navItem = link!.closest('antrea-nav-item') as unknown as { active?: boolean };
        expect(navItem.active).toBeFalsy();
    });
});
