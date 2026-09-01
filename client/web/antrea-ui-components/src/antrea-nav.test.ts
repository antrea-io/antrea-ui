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

import { afterEach, describe, expect, test } from 'vitest';
import './antrea-nav';
import type { AntreaNavGroup } from './antrea-nav';

afterEach(() => {
    document.body.replaceChildren();
});

function mount(hasActiveChild = false): AntreaNavGroup {
    const group = document.createElement('antrea-nav-group') as AntreaNavGroup;
    group.hasActiveChild = hasActiveChild;
    document.body.append(group);
    return group;
}

describe('antrea-nav-group', () => {
    test('starts collapsed when it has no active child', async () => {
        const group = mount();
        await group.updateComplete;

        expect(group.expanded).toBe(false);
    });

    test('starts expanded when the host reports an active child', async () => {
        const group = mount(true);
        await group.updateComplete;

        expect(group.expanded).toBe(true);
    });

    test('clicking the toggle button expands a collapsed group', async () => {
        const group = mount();
        await group.updateComplete;

        const toggle = group.shadowRoot!.querySelector('.group-toggle') as HTMLButtonElement;
        toggle.click();
        await group.updateComplete;

        expect(group.expanded).toBe(true);
    });

    test('clicking the header slot expands a collapsed group', async () => {
        const group = mount();
        const headerLink = document.createElement('a');
        headerLink.slot = 'header';
        headerLink.textContent = 'Flow Visibility';
        group.append(headerLink);
        await group.updateComplete;

        headerLink.click();
        await group.updateComplete;

        expect(group.expanded).toBe(true);
    });

    test('clicking the header slot never collapses an already-expanded group', async () => {
        // Distinct from the toggle button: the header slot also navigates (it's the group's own
        // link), so collapsing on click would hide the page just navigated to.
        const group = mount(true);
        const headerLink = document.createElement('a');
        headerLink.slot = 'header';
        headerLink.textContent = 'Flow Visibility';
        group.append(headerLink);
        await group.updateComplete;

        headerLink.click();
        await group.updateComplete;

        expect(group.expanded).toBe(true);
    });

    test('clicking the toggle button again collapses it back', async () => {
        const group = mount(true);
        await group.updateComplete;

        const toggle = group.shadowRoot!.querySelector('.group-toggle') as HTMLButtonElement;
        toggle.click();
        await group.updateComplete;

        expect(group.expanded).toBe(false);
    });

    test('clicking the toggle button collapses even with a header link present (stopPropagation)', async () => {
        // Without stopPropagation, the click would bubble from the button to the header row and
        // re-expand it there, masking the collapse.
        const group = mount(true);
        const headerLink = document.createElement('a');
        headerLink.slot = 'header';
        headerLink.textContent = 'Flow Visibility';
        group.append(headerLink);
        await group.updateComplete;

        const toggle = group.shadowRoot!.querySelector('.group-toggle') as HTMLButtonElement;
        toggle.click();
        await group.updateComplete;

        expect(group.expanded).toBe(false);
    });

    test('hasActiveChild turning true after mount (not just at mount) expands the group', async () => {
        // Covers a host whose route guard resolves after this element's first render — e.g. a
        // redirect still in flight, or an access summary that hasn't loaded yet — so hasActiveChild
        // starts false and only later becomes true, rather than being true from the start.
        const group = mount(false);
        await group.updateComplete;
        expect(group.expanded).toBe(false);

        group.hasActiveChild = true;
        await group.updateComplete;

        expect(group.expanded).toBe(true);
    });

    test('hasActiveChild flipping after first render does not re-collapse or re-expand the group', async () => {
        const group = mount(true);
        await group.updateComplete;
        expect(group.expanded).toBe(true);

        // Simulates navigating away from the active child without unmounting the group (e.g. a
        // route change elsewhere in the sidebar) — the user's (or the initial auto-expand's)
        // state should stick rather than fight subsequent prop changes.
        group.hasActiveChild = false;
        await group.updateComplete;

        expect(group.expanded).toBe(true);
    });
});
