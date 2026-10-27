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
import { MemoryRouter, Routes, Route, useLocation } from 'react-router';
import { Provider } from 'react-redux';
import { resetAccessSummary } from '@antrea/ui-components';
import type { AccessSummary } from '@antrea/ui-components';
import { setupStore, setSession, setAuthenticated } from './store';
import { AccessProvider, useAccess, useScopeUser } from './access';
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

function Where() {
    const { pathname, search } = useLocation();
    return <div data-testid="where">{pathname + search}</div>;
}

describe('HomeRedirect', () => {
    beforeEach(() => { sessionStorage.clear(); });

    function renderAt(summary: AccessSummary | null) {
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(summary ? jsonResponse(summary) : new Response('', { status: 500 })));
        const store = setupStore({ session: 'authenticated', sessionInfo: { authenticated: true, username: 'alice' } });
        return render(
            <Provider store={store}>
                <AccessProvider>
                    <MemoryRouter initialEntries={['/']}>
                        <Where />
                        <Routes>
                            <Route path="/" element={<HomeRedirect />} />
                            <Route path="/overview" element={<div data-testid="landed">overview</div>} />
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

    test('lands on /overview when canViewOverview is granted', async () => {
        renderAt(summaryWith({
            rules: { resourceRules: [{ apiGroups: [''], resources: ['pods'], verbs: ['list'] }], nonResourceRules: [], incomplete: false },
        }));
        await waitFor(() => expect(document.querySelector('[data-testid="landed"]')?.textContent).toBe('overview'));
    });

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

    // Like a navigation entry, the redirect carries the last scope chosen, but not another user's.
    test('carries the last scope chosen into /flows/list', async () => {
        sessionStorage.setItem('antrea-ui:last-scope', JSON.stringify({ user: 'alice', scope: 'ns-last' }));
        renderAt(summaryWith({
            rules: { resourceRules: [{ apiGroups: ['observability.antrea.io'], resources: ['flows'], verbs: ['watch'] }], nonResourceRules: [], incomplete: false },
        }));
        await waitFor(() => expect(document.querySelector('[data-testid="where"]')?.textContent)
            .toBe('/flows/list?namespace=ns-last'));
    });

    // Both redirects to /overview carry it: the one for a cluster-wide grant, and the one that
    // ranks a caller named in a RoleBinding after the other pages.
    test('carries the last scope chosen into /overview', async () => {
        sessionStorage.setItem('antrea-ui:last-scope', JSON.stringify({ user: 'alice', scope: 'ns-last' }));
        renderAt(summaryWith({
            rules: { resourceRules: [{ apiGroups: [''], resources: ['pods'], verbs: ['list'] }], nonResourceRules: [], incomplete: false },
        }));
        await waitFor(() => expect(document.querySelector('[data-testid="where"]')?.textContent)
            .toBe('/overview?namespace=ns-last'));
    });

    test('carries the last scope chosen into /overview from the RoleBinding fallback', async () => {
        sessionStorage.setItem('antrea-ui:last-scope', JSON.stringify({ user: 'alice', scope: 'ns-last' }));
        renderAt(summaryWith({ namespaces: ['team-a'] }));
        await waitFor(() => expect(document.querySelector('[data-testid="where"]')?.textContent)
            .toBe('/overview?namespace=ns-last'));
    });

    // The grant has to be cluster-wide (see holdsFlowsClusterWide's own doc comment): a caller
    // holding flows only in their own Namespace does not land there, since the redirect is for
    // whoever can observe the whole cluster. The namespace field here stands in for what a
    // namespace-scoped summary would report; it is holdsFlowsClusterWide's own namespace check
    // that must reject it, not the fixture.
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

    // A user who may list namespaces has summary.namespaces == ["*"], which says nothing about
    // RoleBindings, so the Overview is worth offering them (see canViewOverview). It still ranks
    // after the pages they have a grant for, like any other user the Overview may or may not
    // have something for.
    describe('a user who may list namespaces', () => {
        const namespacesList = { apiGroups: [''], resources: ['namespaces'], verbs: ['list'] };
        const lister = (...rules: { apiGroups: string[], resources: string[], verbs: string[] }[]) => summaryWith({
            namespaces: ['*'],
            rules: { resourceRules: [namespacesList, ...rules], nonResourceRules: [], incomplete: false },
        });

        test('lands on /summary when Summary is granted', async () => {
            renderAt(lister({ apiGroups: ['crd.antrea.io'], resources: ['antreaagentinfos'], verbs: ['list'] }));
            await waitFor(() => expect(document.querySelector('[data-testid="landed"]')?.textContent).toBe('summary'));
        });

        test('lands on /overview, not /settings, when that is the only grant', async () => {
            renderAt(lister());
            await waitFor(() => expect(document.querySelector('[data-testid="landed"]')?.textContent).toBe('overview'));
        });
    });

    // summary.namespaces is the RoleBinding-subject heuristic: a superset of what the user can
    // see, which ignores what each binding grants. A user named in any RoleBinding must not be
    // sent to an Overview that may show only an access notice when they have a grant that works.
    describe('a user who is only named in a RoleBinding (summary.namespaces)', () => {
        const named = (rule: { apiGroups: string[], resources: string[], verbs: string[] }) => summaryWith({
            namespaces: ['team-a'],
            rules: { resourceRules: [rule], nonResourceRules: [], incomplete: false },
        });

        test('lands on /summary when Summary is granted', async () => {
            renderAt(named({ apiGroups: ['crd.antrea.io'], resources: ['antreaagentinfos'], verbs: ['list'] }));
            await waitFor(() => expect(document.querySelector('[data-testid="landed"]')?.textContent).toBe('summary'));
        });

        test('lands on /traceflow when Traceflow is granted', async () => {
            renderAt(named({ apiGroups: ['crd.antrea.io'], resources: ['traceflows'], verbs: ['create'] }));
            await waitFor(() => expect(document.querySelector('[data-testid="landed"]')?.textContent).toBe('traceflow'));
        });

        test('lands on /flows/list when Flows is granted', async () => {
            renderAt(named({ apiGroups: ['observability.antrea.io'], resources: ['flows'], verbs: ['watch'] }));
            await waitFor(() => expect(document.querySelector('[data-testid="landed"]')?.textContent).toBe('flows'));
        });

        // Ranked after the others, not dropped: the page may have something for them, and Settings
        // is only the floor.
        test('lands on /overview before falling back to /settings', async () => {
            renderAt(summaryWith({ namespaces: ['team-a'] }));
            await waitFor(() => expect(document.querySelector('[data-testid="landed"]')?.textContent).toBe('overview'));
        });
    });

    test('fails open to /overview when the fetch fails', async () => {
        renderAt(null);
        await waitFor(() => expect(document.querySelector('[data-testid="landed"]')?.textContent).toBe('overview'));
    });
});

describe('useScopeUser', () => {
    function ScopeUserProbe() {
        const { loaded } = useAccess();
        return <><div data-testid="scope-user">{useScopeUser()}</div><div data-testid="loaded">{String(loaded)}</div></>;
    }
    function scopeUser() {
        return document.querySelector('[data-testid="scope-user"]')!.textContent;
    }
    // The summary is what the order of names is about, so wait for it before looking.
    async function summaryLoaded() {
        await waitFor(() => expect(document.querySelector('[data-testid="loaded"]')!.textContent).toBe('true'));
    }

    test('is the session username, else the one the access summary resolved, else empty', async () => {
        // A fresh Response per call: a body can be read once.
        vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(summaryWith({ username: 'system:sa:ns:impersonated' }))));
        const store = setupStore({ session: 'authenticated', sessionInfo: { authenticated: true, mode: 'admin', username: 'admin' } });
        const { unmount } = render(
            <Provider store={store}><AccessProvider><ScopeUserProbe /></AccessProvider></Provider>,
        );
        await summaryLoaded();
        expect(scopeUser()).toBe('admin');
        unmount();

        // The session-info fetch failed: the summary names the user.
        resetAccessSummary();
        const noInfo = setupStore({ session: 'authenticated', sessionInfo: null });
        render(<Provider store={noInfo}><AccessProvider><ScopeUserProbe /></AccessProvider></Provider>);
        await summaryLoaded();
        expect(scopeUser()).toBe('system:sa:ns:impersonated');
    });

    test('is empty when neither name is known', async () => {
        vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('down')));
        const store = setupStore({ session: 'authenticated', sessionInfo: null });
        render(<Provider store={store}><AccessProvider><ScopeUserProbe /></AccessProvider></Provider>);
        await summaryLoaded();
        expect(scopeUser()).toBe('');
    });
});
