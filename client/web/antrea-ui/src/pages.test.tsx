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
import { navigateTo, resetAccessSummary } from '@antrea/ui-components';
import type { AccessSummary } from '@antrea/ui-components';
import { MemoryRouter } from 'react-router';
import { setupStore } from './store';
import { FlowVisibilityPage, SummaryPage } from './pages';
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
                    {/* FlowVisibilityPage reads useSearchParams() for the Overview deep-link
                        filter (see pages.tsx), which throws outside a Router. */}
                    <MemoryRouter><FlowVisibilityPage view="list" /></MemoryRouter>
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
