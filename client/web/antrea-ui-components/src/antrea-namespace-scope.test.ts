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


import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import './antrea-namespace-scope';
import type { AntreaNamespaceScope } from './antrea-namespace-scope';
import { NAMESPACE_SCOPE_STATE_EVENT } from './antrea-namespace-scope';
import type { NamespaceScopeState } from './lib/namespace-scope-controller';
import { resetAccessSummary } from './lib/access-api';

const summary = {
    username: 'alice', groups: [], clusterAdmin: false, namespaces: ['ns-a', 'ns-b'],
    rules: { resourceRules: [], nonResourceRules: [], incomplete: false },
};

beforeEach(() => {
    window.history.replaceState(null, '', '/');
    vi.stubGlobal('fetch', vi.fn().mockImplementation(
        () => Promise.resolve(new Response(JSON.stringify(summary), { status: 200 }))));
});

afterEach(() => {
    document.body.innerHTML = '';
    vi.unstubAllGlobals();
    resetAccessSummary();
});

// A plugin page: its own shadow root around the element, as the host mounts a plugin's tag.
function mount(props: Partial<AntreaNamespaceScope> = {}) {
    const plugin = document.createElement('div');
    const root = plugin.attachShadow({ mode: 'open' });
    const el = document.createElement('antrea-namespace-scope');
    Object.assign(el, props);
    root.appendChild(el);
    document.body.appendChild(plugin);
    return { plugin, el };
}

describe('antrea-namespace-scope', () => {
    it('emits one initial state each time it connects', () => {
        const el = document.createElement('antrea-namespace-scope');
        el.locationSearch = '?namespace=ns-a';
        const listener = vi.fn();
        el.addEventListener(NAMESPACE_SCOPE_STATE_EVENT, listener);
        document.body.appendChild(el);
        expect(listener).toHaveBeenCalledTimes(1);
        expect(listener.mock.calls[0][0].detail).toBe(el.scope);
        el.remove();
        document.body.appendChild(el);
        expect(listener).toHaveBeenCalledTimes(2);
    });

    it('renders a styled picker in its own shadow root', async () => {
        const { el } = mount({ locationSearch: '' });
        await el.updateComplete;
        const picker = el.shadowRoot!.querySelector('antrea-namespace-picker');
        expect(picker).not.toBeNull();
        // Lit uses adopted stylesheets when supported and <style> elements otherwise.
        const root = el.shadowRoot!;
        const css = [
            ...Array.from(root.adoptedStyleSheets ?? []).flatMap(sheet => Array.from(sheet.cssRules, rule => rule.cssText)),
            ...Array.from(root.querySelectorAll('style'), style => style.textContent),
        ].join('\n');
        expect(css).toContain('.combobox-list');
        expect(css).toContain('.field-input');
    });

    it('offers the Namespace list once it has loaded', async () => {
        const { el } = mount({ locationSearch: '' });
        await vi.waitFor(() => expect(el.scope?.access?.candidates).toEqual(['ns-a', 'ns-b']));
        await el.updateComplete;
        expect(el.scope?.scope).toEqual({ kind: 'namespace', namespace: 'ns-a' });
    });

    it('accepts the anyOf resource form used by a Namespace-aware route', async () => {
        const { el } = mount({ locationSearch: '', resources: { anyOf: [{ group: '', resource: 'pods', verb: 'list' }] } });
        await vi.waitFor(() => expect(el.scope?.access).not.toBeNull());
        expect(el.scope?.access?.clusterWide).toBe('denied');
        expect(el.scope?.offerAll).toBe(false);
    });

    it('publishes only the new scope when resources and location change together', async () => {
        vi.stubGlobal('fetch', vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify({
            ...summary,
            rules: { resourceRules: [{ verbs: ['list'], apiGroups: [''], resources: ['pods'] }], nonResourceRules: [], incomplete: false },
        }), { status: 200 }))));
        const { el } = mount({ locationSearch: '?namespace=_all', resources: [{ group: '', resource: 'pods', verb: 'list' }] });
        await vi.waitFor(() => expect(el.scope?.access?.clusterWide).toBe('allowed'));
        const scopes: unknown[] = [];
        el.addEventListener(NAMESPACE_SCOPE_STATE_EVENT, e => scopes.push((e as CustomEvent<NamespaceScopeState>).detail.scope));
        el.locationSearch = '';
        el.resources = [{ group: '', resource: 'services', verb: 'list' }];
        await vi.waitFor(() => expect(el.scope?.access?.clusterWide).toBe('denied'));
        expect(el.scope?.scope).toEqual({ kind: 'namespace', namespace: 'ns-a' });
        expect(scopes).toEqual([null, { kind: 'namespace', namespace: 'ns-a' }]);
    });

    it('lets the host events cross the plugin shadow root, and keeps the state event inside', async () => {
        const outer = vi.fn();
        const state = vi.fn();
        document.addEventListener('antrea-namespace-scope-chosen', outer);
        document.addEventListener(NAMESPACE_SCOPE_STATE_EVENT, state);
        const { plugin, el } = mount({ locationSearch: '?namespace=ns-b' });
        const inPlugin = vi.fn();
        plugin.shadowRoot!.addEventListener(NAMESPACE_SCOPE_STATE_EVENT, inPlugin);
        await vi.waitFor(() => expect(el.scope?.access).not.toBeNull());
        expect(outer).toHaveBeenCalled();
        expect((outer.mock.calls[0][0] as CustomEvent).detail).toEqual({ scope: 'ns-b' });
        // Bubbles to the plugin's own root, but not out of it.
        expect(state).not.toHaveBeenCalled();
        document.removeEventListener('antrea-namespace-scope-chosen', outer);
        document.removeEventListener(NAMESPACE_SCOPE_STATE_EVENT, state);
    });

    it('announces its state, and follows locationSearch', async () => {
        const { plugin, el } = mount({ locationSearch: '?namespace=ns-a' });
        const states: NamespaceScopeState[] = [];
        plugin.shadowRoot!.addEventListener(NAMESPACE_SCOPE_STATE_EVENT, e =>
            states.push((e as CustomEvent<NamespaceScopeState>).detail));
        el.locationSearch = '?namespace=_all';
        await el.updateComplete;
        expect(states.at(-1)?.scope).toEqual({ kind: 'all' });
    });

    it('shows an invalid scope of the URL as an error', async () => {
        const { el } = mount({ locationSearch: '?namespace=Bad_Name' });
        await el.updateComplete;
        expect(el.shadowRoot!.querySelector('antrea-alert')?.textContent).toContain('not a valid namespace name');
    });

    it('writes a pick to the URL', async () => {
        const { el } = mount({ locationSearch: '' });
        await vi.waitFor(() => expect(el.scope?.access).not.toBeNull());
        await el.updateComplete;
        const picker = el.shadowRoot!.querySelector('antrea-namespace-picker')!;
        picker.dispatchEvent(new CustomEvent('antrea-scope-select', { detail: { value: 'ns-b', typed: false } }));
        expect(window.location.search).toBe('?namespace=ns-b');
        expect(el.scope?.scope).toEqual({ kind: 'namespace', namespace: 'ns-b' });
    });

    it('stops its controller when removed', async () => {
        const { plugin, el } = mount({ locationSearch: '' });
        await el.updateComplete;
        plugin.remove();
        expect(el.scope).toBeNull();
    });
});
