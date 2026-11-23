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

import { act, render, waitFor } from '@testing-library/react';
import { Provider } from 'react-redux';
import { MemoryRouter, useLocation } from 'react-router';
import { resetAccessSummary } from '@antrea/ui-components';
import type { AntreaNamespaceScope } from '@antrea/ui-components';
import { AccessProvider } from './access';
import { PluginPage } from './pages';
import { getLastScope } from './lastScope';
import { setupStore } from './store';

// Let Vite load the standalone example without including its source in the host's TS project.
const examples = import.meta.glob('../../../../plugins/examples/pod-counter/src/index.ts');
beforeAll(async () => { await examples['../../../../plugins/examples/pod-counter/src/index.ts'](); });

const PODS = [{ group: '', resource: 'pods', verb: 'list' }];
const summary = {
    username: 'alice', groups: [], clusterAdmin: false, namespaces: ['ns-a', 'ns-b'],
    rules: { resourceRules: [], nonResourceRules: [], incomplete: false },
};
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

afterEach(() => { vi.unstubAllGlobals(); resetAccessSummary(); sessionStorage.clear(); });

async function mount(loadPods: (url: string, init?: RequestInit) => Promise<Response>) {
    const podFetch = vi.fn(loadPods);
    vi.stubGlobal('fetch', vi.fn((url: string, init?: RequestInit) => url.includes('/access-summary')
        ? Promise.resolve(json(summary)) : podFetch(url, init)));
    function Location() {
        const { pathname, search } = useLocation();
        return <span data-testid="location">{pathname + search}</span>;
    }
    const view = render(
        <Provider store={setupStore({ session: 'authenticated' })}>
            <AccessProvider>
                <MemoryRouter initialEntries={['/plugin/pod-counter?namespace=ns-a&view=1']}>
                    <Location />
                    <PluginPage tag="antrea-plugin-pod-counter" access={[{ namespaceScope: true, resources: PODS }]} />
                </MemoryRouter>
            </AccessProvider>
        </Provider>,
    );
    await waitFor(() => expect(podFetch).toHaveBeenCalled());
    const page = document.querySelector('antrea-plugin-pod-counter')!;
    const picker = page.shadowRoot!.querySelector('antrea-namespace-scope') as AntreaNamespaceScope;
    await waitFor(() => expect(picker.scope?.access).not.toBeNull());
    return { page, picker, podFetch, view };
}

async function pick(picker: AntreaNamespaceScope, value: string) {
    await act(async () => {
        picker.shadowRoot!.querySelector('antrea-namespace-picker')!.dispatchEvent(
            new CustomEvent('antrea-scope-select', { detail: { value, typed: false } }));
    });
}

test('the example follows the scope without reloading for access or note notifications', async () => {
    const { page, picker, podFetch, view } = await mount(async () => json({ items: [{}] }));
    await waitFor(() => expect(page.shadowRoot!.textContent).toContain('Pods in ns-a: 1'));
    expect(podFetch).toHaveBeenCalledTimes(1);
    expect(podFetch.mock.calls[0][0]).toBe('/api/v1/k8s/api/v1/namespaces/ns-a/pods');
    await pick(picker, 'ns-z');
    await waitFor(() => expect(picker.scope?.note?.kind).toBe('not-listed'));
    expect(podFetch).toHaveBeenCalledTimes(2);
    expect(view.getByTestId('location')).toHaveTextContent('/plugin/pod-counter?namespace=ns-z&view=1');
    expect(getLastScope('alice')).toBe('ns-z');
    await pick(picker, 'ns-z');
    expect(podFetch).toHaveBeenCalledTimes(2);
    await pick(picker, '__cluster_wide__');
    expect(podFetch.mock.calls[podFetch.mock.calls.length - 1][0]).toBe('/api/v1/k8s/api/v1/pods');
});

test('the example cancels stale work and ignores its late result after a scope change', async () => {
    let resolveOld!: (response: Response) => void;
    const old = new Promise<Response>(resolve => { resolveOld = resolve; });
    const { page, picker, podFetch, view } = await mount(async url => url.includes('/ns-a/') ? old : json({ items: [{}, {}] }));
    const oldSignal = podFetch.mock.calls[0][1]?.signal;
    await pick(picker, 'ns-b');
    expect(oldSignal?.aborted).toBe(true);
    await waitFor(() => expect(page.shadowRoot!.textContent).toContain('Pods in ns-b: 2'));
    await act(async () => { resolveOld(json({ items: [{}] })); });
    expect(page.shadowRoot!.textContent).toContain('Pods in ns-b: 2');
    const currentSignal = podFetch.mock.calls[1][1]?.signal;
    view.unmount();
    expect(currentSignal?.aborted).toBe(true);
});

test('a refusal keeps the picker available and an explicit retry reloads the same scope', async () => {
    let denied = true;
    const { page, picker, podFetch } = await mount(async () => denied ? json({}, 403) : json({ items: [{}] }));
    await waitFor(() => expect(page.shadowRoot!.textContent).toContain('You do not have permission'));
    expect(picker.scope?.scope).toEqual({ kind: 'namespace', namespace: 'ns-a' });
    denied = false;
    await act(async () => { page.shadowRoot!.querySelector('button')!.click(); });
    await waitFor(() => expect(page.shadowRoot!.textContent).toContain('Pods in ns-a: 1'));
    expect(podFetch).toHaveBeenCalledTimes(2);
});
