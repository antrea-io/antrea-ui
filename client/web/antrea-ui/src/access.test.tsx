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

import { act, render, waitFor } from '@testing-library/react';
import { MemoryRouter, Routes, Route } from 'react-router';
import { Provider } from 'react-redux';
import { resetAccessSummary } from '@antrea/ui-components';
import type { AccessSummary } from '@antrea/ui-components';
import { setupStore, setSession, setAuthenticated } from './store';
import { AccessProvider, useAccess } from './access';
import { HomeRedirect } from './pages';

function jsonResponse(body: unknown, status = 200): Response {
    return new Response(JSON.stringify(body), { status });
}

function summaryWith(overrides: Partial<AccessSummary> = {}): AccessSummary {
    return {
        username: 'alice',
        groups: [],
        clusterAdmin: false,
        rules: { resourceRules: [], nonResourceRules: [], incomplete: false },
        namespaces: [],
        ...overrides,
    };
}

function Probe() {
    const { summary, loaded } = useAccess();
    return <div data-testid="probe">{JSON.stringify({ loaded, username: summary?.username ?? null })}</div>;
}

afterEach(() => {
    vi.unstubAllGlobals();
    resetAccessSummary();
});

describe('AccessProvider', () => {
    test('fetches accessSummary once the session is authenticated', async () => {
        const fetchMock = vi.fn().mockResolvedValue(jsonResponse(summaryWith()));
        vi.stubGlobal('fetch', fetchMock);
        const store = setupStore({ session: 'authenticated' });

        render(
            <Provider store={store}>
                <AccessProvider><Probe /></AccessProvider>
            </Provider>,
        );

        await waitFor(() => expect(document.querySelector('[data-testid="probe"]')?.textContent)
            .toContain('"loaded":true'));
        expect(fetchMock).toHaveBeenCalledTimes(1);
        expect(document.querySelector('[data-testid="probe"]')?.textContent).toContain('alice');
    });

    test('does not fetch while the session is not authenticated', () => {
        const fetchMock = vi.fn();
        vi.stubGlobal('fetch', fetchMock);
        const store = setupStore({ session: 'unknown' });

        render(
            <Provider store={store}>
                <AccessProvider><Probe /></AccessProvider>
            </Provider>,
        );

        expect(fetchMock).not.toHaveBeenCalled();
        expect(document.querySelector('[data-testid="probe"]')?.textContent).toContain('"loaded":false');
    });

    test('a fetch failure fails open: loaded becomes true with a null summary', async () => {
        vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('network down')));
        const store = setupStore({ session: 'authenticated' });

        render(
            <Provider store={store}>
                <AccessProvider><Probe /></AccessProvider>
            </Provider>,
        );

        await waitFor(() => expect(document.querySelector('[data-testid="probe"]')?.textContent)
            .toContain('"loaded":true'));
        expect(document.querySelector('[data-testid="probe"]')?.textContent).toContain('"username":null');
    });

    test('resetAccessSummary() followed by re-authentication re-fetches', async () => {
        const fetchMock = vi.fn().mockResolvedValue(jsonResponse(summaryWith()));
        vi.stubGlobal('fetch', fetchMock);
        const store = setupStore({ session: 'authenticated' });

        render(
            <Provider store={store}>
                <AccessProvider><Probe /></AccessProvider>
            </Provider>,
        );
        await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));

        // Simulate the logout->login cycle: useLogout() and the re-auth listener in App.tsx
        // both call resetAccessSummary() before flipping the session state.
        resetAccessSummary();
        act(() => { store.dispatch(setSession('anonymous')); });
        act(() => { store.dispatch(setAuthenticated(null)); });

        await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    });

    test('leaving the authenticated session clears the previous summary', async () => {
        const fetchMock = vi.fn().mockResolvedValue(jsonResponse(summaryWith()));
        vi.stubGlobal('fetch', fetchMock);
        const store = setupStore({ session: 'authenticated' });

        render(
            <Provider store={store}>
                <AccessProvider><Probe /></AccessProvider>
            </Provider>,
        );
        await waitFor(() => expect(document.querySelector('[data-testid="probe"]')?.textContent)
            .toContain('alice'));

        act(() => { store.dispatch(setSession('anonymous')); });

        // Not just loaded:false — the next user must never see alice's permissions, not even
        // for the frame between authenticating and their own summary arriving.
        const text = document.querySelector('[data-testid="probe"]')?.textContent;
        expect(text).toContain('"loaded":false');
        expect(text).toContain('"username":null');
    });
});

describe('HomeRedirect', () => {
    function renderAt(summary: AccessSummary | null) {
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(summary ? jsonResponse(summary) : new Response('', { status: 500 })));
        const store = setupStore({ session: 'authenticated' });
        return render(
            <Provider store={store}>
                <AccessProvider>
                    <MemoryRouter initialEntries={['/']}>
                        <Routes>
                            <Route path="/" element={<HomeRedirect />} />
                            <Route path="/summary" element={<div data-testid="landed">summary</div>} />
                            <Route path="/traceflow" element={<div data-testid="landed">traceflow</div>} />
                            <Route path="/flows/list" element={<div data-testid="landed">flows</div>} />
                            <Route path="/settings" element={<div data-testid="landed">settings</div>} />
                        </Routes>
                    </MemoryRouter>
                </AccessProvider>
            </Provider>,
        );
    }

    test('lands on /summary when canViewSummary is granted', async () => {
        renderAt(summaryWith({
            rules: { resourceRules: [{ apiGroups: ['crd.antrea.io'], resources: ['antreaagentinfos'], verbs: ['list'] }], nonResourceRules: [], incomplete: false },
        }));
        await waitFor(() => expect(document.querySelector('[data-testid="landed"]')?.textContent).toBe('summary'));
    });

    test('lands on /traceflow when only Traceflow is granted', async () => {
        renderAt(summaryWith({
            rules: { resourceRules: [{ apiGroups: ['crd.antrea.io'], resources: ['traceflows'], verbs: ['create'] }], nonResourceRules: [], incomplete: false },
        }));
        await waitFor(() => expect(document.querySelector('[data-testid="landed"]')?.textContent).toBe('traceflow'));
    });

    test('falls back to /settings when neither Summary, Traceflow nor Flows is granted', async () => {
        // Settings needs no permission at all, so it is the floor here instead of a guess that
        // could land on a 403.
        renderAt(summaryWith());
        await waitFor(() => expect(document.querySelector('[data-testid="landed"]')?.textContent).toBe('settings'));
    });

    test('lands on /flows/list for a caller holding the flows watch grant', async () => {
        renderAt(summaryWith({
            rules: { resourceRules: [{ apiGroups: ['observability.antrea.io'], resources: ['flows'], verbs: ['watch'] }], nonResourceRules: [], incomplete: false },
        }));
        await waitFor(() => expect(document.querySelector('[data-testid="landed"]')?.textContent).toBe('flows'));
    });

    // The grant has to be cluster-wide (see canViewFlows's own doc comment): a caller holding
    // flows only in their own Namespace does not land there, because clusterWide=true is the
    // only scope the page can request today. The namespace field here stands in for what a
    // namespace-scoped summary would report; it is canViewFlows's own namespace check that must
    // reject it, not the fixture.
    test('does not land on /flows/list for a caller holding only a namespaced flows grant', async () => {
        renderAt(summaryWith({
            namespace: 'default',
            rules: {
                resourceRules: [{ apiGroups: ['observability.antrea.io'], resources: ['flows'], verbs: ['watch'] }],
                nonResourceRules: [],
                incomplete: false,
            },
        }));
        await waitFor(() => expect(document.querySelector('[data-testid="landed"]')?.textContent).toBe('settings'));
    });

    test('fails open to /summary when the fetch fails', async () => {
        renderAt(null);
        await waitFor(() => expect(document.querySelector('[data-testid="landed"]')?.textContent).toBe('summary'));
    });
});
