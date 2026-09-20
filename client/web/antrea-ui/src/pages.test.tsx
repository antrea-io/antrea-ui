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
import { Provider } from 'react-redux';
import { navigateTo, resetAccessSummary, SCOPE_CHANGE_EVENT } from '@antrea/ui-components';
import type { AccessSummary } from '@antrea/ui-components';
import { setAuthenticated, setupStore } from './store';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { FlowVisibilityPage, OverviewPage, SummaryPage } from './pages';
import type { LandingPageTab } from './plugins';
import { AccessProvider } from './access';

// AntreaSummaryPage is a Lit web component with its own shadow DOM; we only need
// its host element here to dispatch the antrea-session-expired event.

// jsdom does not implement cross-document navigation; record the logout redirect issued by
// useLogout() instead.
vi.mock('../../antrea-ui-components/src/lib/navigation.js', () => ({ navigateTo: vi.fn() }));

afterEach(() => {
    vi.unstubAllGlobals();
    // accessSummary() memoizes its result across calls; without this, the second
    // "FlowVisibilityPage — route guard" test below would reuse the first one's cached (denied)
    // summary instead of fetching the one it stubs.
    resetAccessSummary();
});

describe('useLitPage — antrea-session-expired', () => {
    // There is no probe-then-retry any more. Credential refresh happens server-side, and the
    // backend has already attempted the only refresh that exists, so a 401 is authoritative:
    // the session is gone and the user has to log in again.
    test('logs the user out immediately, without probing the backend first', async () => {
        const store = setupStore({ session: 'authenticated' });
        const fetchMock = vi.fn(async (url: string) => {
            throw new Error(`unexpected fetch to ${url}`);
        });
        vi.stubGlobal('fetch', fetchMock);
        vi.mocked(navigateTo).mockClear();

        render(<Provider store={store}><AccessProvider><SummaryPage /></AccessProvider></Provider>);
        // The access summary fetch fails (fetchMock throws for every URL) and fails open,
        // so the page renders once that resolves.
        await waitFor(() => expect(document.querySelector('antrea-summary-page')).not.toBeNull());
        const el = document.querySelector('antrea-summary-page')!;

        await act(async () => {
            el.dispatchEvent(new CustomEvent('antrea-session-expired'));
        });

        await waitFor(() => expect(store.getState().session).toBe('anonymous'));
        expect(navigateTo).toHaveBeenCalledTimes(1);
        const redirect = vi.mocked(navigateTo).mock.calls[0][0];
        expect(redirect).toContain('/auth/logout?');
        // The message is nested inside the redirect_url parameter, hence double-encoded.
        expect(decodeURIComponent(redirect)).toContain('session+has+expired');
        // No /auth/* round-trip: the old code tried a token refresh here first.
        expect(fetchMock.mock.calls.filter(([url]) => url.startsWith('/auth/'))).toHaveLength(0);
    });
});

// The route guard, as opposed to the nav entry. nav.test.tsx covers the entry, but the route is
// the actual enforcement point: a user who bookmarked /flows/list or typed it never goes near the
// nav. This uses the real gate through AccessProvider, so it also pins that the page and the nav
// read the same rule rather than two that happen to agree today.
describe('FlowVisibilityPage — route guard', () => {
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

    function renderPage(summary: AccessSummary) {
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
            new Response(JSON.stringify(summary), { status: 200 })));
        const store = setupStore({ session: 'authenticated' });
        return render(
            <Provider store={store}>
                <AccessProvider>
                    <MemoryRouter>
                        <FlowVisibilityPage view="list" />
                    </MemoryRouter>
                </AccessProvider>
            </Provider>,
        );
    }

    test('renders the permission panel, and never the page, for a denied user', async () => {
        renderPage(summaryWith());
        await waitFor(() => expect(document.querySelector('antrea-alert')).not.toBeNull());
        expect(document.querySelector('antrea-alert')!.textContent)
            .toContain('You do not have permission to view this page');
        // The point of the guard: the Lit element is never constructed, so it never opens the
        // stream. Asserting the panel alone would pass with both rendered.
        expect(document.querySelector('antrea-flow-visibility-page')).toBeNull();
    });

    test('renders the page for a user granted the flows watch gate', async () => {
        renderPage(summaryWith({
            rules: { resourceRules: [{ apiGroups: ['observability.antrea.io'], resources: ['flows'], verbs: ['watch'] }], nonResourceRules: [], incomplete: false },
        }));
        await waitFor(() => expect(document.querySelector('antrea-flow-visibility-page')).not.toBeNull());
        expect(document.querySelector('antrea-alert')).toBeNull();
    });
});

// The Overview landing page: the shell renders the welcome title and the tab strip, and mounts
// the Lit element for the active tab. The tiles themselves are covered by antrea-overview-page's
// own tests; what is pinned here is what this component decides.
describe('OverviewPage', () => {
    // Defined once for the file: a custom element name cannot be registered twice.
    class PluginTabElement extends HTMLElement {}
    customElements.define('antrea-test-security-tab', PluginTabElement);

    const pluginTabs: LandingPageTab[] = [
        { id: 'security', label: 'Security', tag: 'antrea-test-security-tab' },
    ];
    const listPods = {
        apiGroups: [''], resources: ['pods'], verbs: ['list'],
    };
    const granted: AccessSummary = {
        username: 'alice', groups: [], clusterAdmin: false, namespaces: [],
        rules: { resourceRules: [listPods], nonResourceRules: [], incomplete: false },
    };
    const denied: AccessSummary = { ...granted, rules: { resourceRules: [], nonResourceRules: [], incomplete: false } };

    function LocationProbe() {
        const { pathname, search } = useLocation();
        return <div data-testid="location">{pathname + search}</div>;
    }

    function renderOverview(opts: {
        summary?: AccessSummary; entry?: string; tabs?: LandingPageTab[]; username?: string; mode?: 'admin' | 'oidc';
    } = {}) {
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
            new Response(JSON.stringify(opts.summary ?? granted), { status: 200 })));
        const store = setupStore({ session: 'authenticated' });
        if (opts.username) store.dispatch(setAuthenticated({ authenticated: true, username: opts.username, mode: opts.mode }));
        render(
            <Provider store={store}>
                <AccessProvider>
                    <MemoryRouter initialEntries={[opts.entry ?? '/overview']}>
                        <LocationProbe />
                        <Routes>
                            <Route path="/overview" element={<OverviewPage pluginTabs={opts.tabs} />} />
                        </Routes>
                    </MemoryRouter>
                </AccessProvider>
            </Provider>,
        );
        return store;
    }

    const tabLabels = () => Array.from(document.querySelectorAll('.tab-bar-item')).map(b => b.textContent);
    const activeTab = () => document.querySelector('.tab-bar-item.active')?.textContent;
    const location = () => document.querySelector('[data-testid="location"]')?.textContent;

    describe('route guard', () => {
        test('renders the permission panel, and never the page, for a user with nothing to see', async () => {
            renderOverview({ summary: denied });
            await waitFor(() => expect(document.querySelector('antrea-alert')).not.toBeNull());
            expect(document.querySelector('antrea-alert')!.textContent)
                .toContain('You do not have permission to view this page');
            expect(document.querySelector('antrea-overview-page')).toBeNull();
            expect(document.querySelector('.tab-bar')).toBeNull();
        });

        test('renders the page for a user who may list something it counts', async () => {
            renderOverview();
            await waitFor(() => expect(document.querySelector('antrea-overview-page')).not.toBeNull());
            expect(document.querySelector('antrea-alert')).toBeNull();
        });

        // The case canViewOverview exists for: summary.namespaces is ["*"], which says nothing
        // about RoleBindings.
        test('renders the page for a user who may only list namespaces', async () => {
            renderOverview({
                summary: {
                    ...denied, namespaces: ['*'],
                    rules: {
                        resourceRules: [{ apiGroups: [''], resources: ['namespaces'], verbs: ['list'] }],
                        nonResourceRules: [], incomplete: false,
                    },
                },
            });
            await waitFor(() => expect(document.querySelector('antrea-overview-page')).not.toBeNull());
        });
    });

    describe('welcome', () => {
        test('greets by the name the header shows', async () => {
            renderOverview({ username: 'alice', mode: 'oidc' });
            await waitFor(() => expect(document.querySelector('.page-title')).not.toBeNull());
            expect(document.querySelector('.page-title')!.textContent).toBe('Welcome, alice');
        });

        // Not the raw Kubernetes username, which for a Service Account is the whole
        // system:serviceaccount:<namespace>:<name>.
        test('uses the friendly form of a Service Account identity', async () => {
            renderOverview({ username: 'system:serviceaccount:team-a:bot', mode: 'oidc' });
            await waitFor(() => expect(document.querySelector('.page-title')).not.toBeNull());
            expect(document.querySelector('.page-title')!.textContent).toBe('Welcome, team-a:bot');
        });

        test('greets without a name when the session has none', async () => {
            renderOverview();
            await waitFor(() => expect(document.querySelector('.page-title')).not.toBeNull());
            expect(document.querySelector('.page-title')!.textContent).toBe('Welcome');
        });
    });

    describe('tab strip', () => {
        test('shows the built-in tab alone when no plugin registered one', async () => {
            renderOverview();
            await waitFor(() => expect(tabLabels()).toEqual(['Network Traffic & Inventory']));
            expect(activeTab()).toBe('Network Traffic & Inventory');
            expect(document.querySelector('antrea-overview-page')).not.toBeNull();
        });

        test('lists a plugin tab after the built-in one', async () => {
            renderOverview({ tabs: pluginTabs });
            await waitFor(() => expect(tabLabels()).toEqual(['Network Traffic & Inventory', 'Security']));
        });

        test('selects a tab through ?tab=, mounting the plugin tab\'s element instead of the built-in page', async () => {
            renderOverview({ tabs: pluginTabs, entry: '/overview?tab=security' });
            await waitFor(() => expect(document.querySelector('antrea-test-security-tab')).not.toBeNull());
            expect(activeTab()).toBe('Security');
            expect(document.querySelector('antrea-overview-page')).toBeNull();
        });

        test('falls back to the built-in tab for an id nothing registered', async () => {
            renderOverview({ tabs: pluginTabs, entry: '/overview?tab=nope' });
            await waitFor(() => expect(document.querySelector('antrea-overview-page')).not.toBeNull());
            expect(activeTab()).toBe('Network Traffic & Inventory');
            expect(document.querySelector('antrea-test-security-tab')).toBeNull();
        });

        test('clicking a tab records it in the URL, and the built-in tab clears it', async () => {
            renderOverview({ tabs: pluginTabs });
            await waitFor(() => expect(tabLabels()).toHaveLength(2));

            await act(async () => {
                (document.querySelectorAll('.tab-bar-item')[1] as HTMLElement).click();
            });
            await waitFor(() => expect(location()).toBe('/overview?tab=security'));
            expect(document.querySelector('antrea-test-security-tab')).not.toBeNull();

            await act(async () => {
                (document.querySelectorAll('.tab-bar-item')[0] as HTMLElement).click();
            });
            await waitFor(() => expect(location()).toBe('/overview'));
            expect(document.querySelector('antrea-overview-page')).not.toBeNull();
        });
    });

    // A plugin's tab is a page like any other: a 401 inside it means the session is gone, and the
    // shell logs the user out the way it does for the built-in pages.
    test('a plugin tab\'s element can end the session', async () => {
        vi.mocked(navigateTo).mockClear();
        const store = renderOverview({ tabs: pluginTabs, entry: '/overview?tab=security' });
        await waitFor(() => expect(document.querySelector('antrea-test-security-tab')).not.toBeNull());

        await act(async () => {
            document.querySelector('antrea-test-security-tab')!
                .dispatchEvent(new CustomEvent('antrea-session-expired'));
        });

        await waitFor(() => expect(store.getState().session).toBe('anonymous'));
        expect(navigateTo).toHaveBeenCalledTimes(1);
    });
});

// The flow page keeps its observed namespace in the query string and rewrites it itself, which no
// router is told about. The host adopts it on the page's own event, so that what the sidebar links
// carry between the two sub-pages is the scope in force and not the one the page loaded with.
describe('FlowVisibilityPage — scope in the URL', () => {
    function LocationProbe() {
        const { pathname, search } = useLocation();
        return <div data-testid="location">{pathname + search}</div>;
    }

    test('the router adopts the query string the page announces', async () => {
        const store = setupStore({ session: 'authenticated' });
        // Every fetch fails, so the access answers fail open and the page renders.
        vi.stubGlobal('fetch', vi.fn(async (url: string) => {
            throw new Error(`unexpected fetch to ${url}`);
        }));

        render(
            <Provider store={store}>
                <AccessProvider>
                    <MemoryRouter initialEntries={['/flows/list']}>
                        <LocationProbe />
                        <Routes>
                            <Route path="/flows/list" element={<FlowVisibilityPage view="list" />} />
                        </Routes>
                    </MemoryRouter>
                </AccessProvider>
            </Provider>,
        );
        await waitFor(() => expect(document.querySelector('antrea-flow-visibility-page')).not.toBeNull());
        const el = document.querySelector('antrea-flow-visibility-page')!;
        expect(document.querySelector('[data-testid="location"]')?.textContent).toBe('/flows/list');

        await act(async () => {
            el.dispatchEvent(new CustomEvent(SCOPE_CHANGE_EVENT, {
                detail: { search: '?observedNamespace=ns-a' }, bubbles: true, composed: true,
            }));
        });

        await waitFor(() => expect(document.querySelector('[data-testid="location"]')?.textContent)
            .toBe('/flows/list?observedNamespace=ns-a'));
    });
});
