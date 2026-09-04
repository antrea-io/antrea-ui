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
import type { RootState } from './store';
import { AccessProvider, useAccess, useCanViewFlows } from './access';
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

// useCanViewFlows is the interim admin-only rule for flow data: the built-in admin, or a
// Kubernetes cluster admin. It mirrors requireFlowVisibility() in pkg/server/api/flowstream.go and,
// unlike the can() gates, fails closed.
describe('useCanViewFlows', () => {
    function FlowProbe() {
        const { allowed, loaded } = useCanViewFlows();
        return <div data-testid="probe">{JSON.stringify({ allowed, loaded })}</div>;
    }

    async function renderProbe(summary: AccessSummary | null, sessionInfo: RootState['sessionInfo']) {
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(summary ? jsonResponse(summary) : new Response('', { status: 500 })));
        const store = setupStore({ session: 'authenticated', sessionInfo });
        render(
            <Provider store={store}>
                <AccessProvider><FlowProbe /></AccessProvider>
            </Provider>,
        );
        await waitFor(() => expect(document.querySelector('[data-testid="probe"]')?.textContent)
            .toContain('"loaded":true'));
        return document.querySelector('[data-testid="probe"]')!.textContent!;
    }

    const adminSession = { authenticated: true, mode: 'admin' as const, username: 'admin' };
    const tokenSession = { authenticated: true, mode: 'token' as const, username: 'alice' };

    test('the built-in admin is allowed even though clusterAdmin is false', async () => {
        // Not redundant with the clusterAdmin term: the static-admin session impersonates the
        // antrea-ui-admin ServiceAccount, whose aggregated ClusterRole holds no */*/* rule, so
        // the wildcard review genuinely answers false for it.
        expect(await renderProbe(summaryWith({ clusterAdmin: false }), adminSession)).toContain('"allowed":true');
    });

    test('a cluster admin is allowed', async () => {
        expect(await renderProbe(summaryWith({ clusterAdmin: true }), tokenSession)).toContain('"allowed":true');
    });

    test('an ordinary user is denied', async () => {
        expect(await renderProbe(summaryWith({ clusterAdmin: false }), tokenSession)).toContain('"allowed":false');
    });

    test('a null summary (fetch failed) fails closed', async () => {
        expect(await renderProbe(null, tokenSession)).toContain('"allowed":false');
    });

    test('a null sessionInfo fails closed', async () => {
        expect(await renderProbe(summaryWith({ clusterAdmin: false }), null)).toContain('"allowed":false');
    });
});

describe('HomeRedirect', () => {
    function renderAt(summary: AccessSummary | null, sessionInfo: RootState['sessionInfo'] = null) {
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(summary ? jsonResponse(summary) : new Response('', { status: 500 })));
        const store = setupStore({ session: 'authenticated', sessionInfo });
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

    test('lands on /flows/list when only flow visibility is permitted', async () => {
        renderAt(summaryWith({ clusterAdmin: true }), { authenticated: true, mode: 'token', username: 'alice' });
        await waitFor(() => expect(document.querySelector('[data-testid="landed"]')?.textContent).toBe('flows'));
    });

    test('falls back to /settings when nothing else is permitted', async () => {
        // Flow Visibility is no longer the floor: it is gated too, so a user permitted none of
        // the three lands on Settings, which needs no permission.
        renderAt(summaryWith());
        await waitFor(() => expect(document.querySelector('[data-testid="landed"]')?.textContent).toBe('settings'));
    });

    test('fails open to /summary when the fetch fails', async () => {
        renderAt(null);
        await waitFor(() => expect(document.querySelector('[data-testid="landed"]')?.textContent).toBe('summary'));
    });
});
