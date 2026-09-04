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
import { setupStore } from './store';
import { FlowVisibilityPage, SummaryPage } from './pages';
import { AccessProvider } from './access';
import { resetAccessSummary } from '@antrea/ui-components';
import type { AccessSummary } from '@antrea/ui-components';
import type { RootState } from './store';

// AntreaSummaryPage is a Lit web component with its own shadow DOM; we only need
// its host element here to dispatch the antrea-session-expired event.

function stubLocationHref() {
    const hrefSetter = vi.fn();
    const originalLocation = Object.getOwnPropertyDescriptor(window, 'location');
    Object.defineProperty(window, 'location', {
        value: new Proxy(window.location, {
            set(target, prop, value) {
                if (prop === 'href') { hrefSetter(value); return true; }
                return Reflect.set(target, prop, value);
            },
        }),
        configurable: true,
    });
    return {
        hrefSetter,
        restore: () => { if (originalLocation) Object.defineProperty(window, 'location', originalLocation); },
    };
}

afterEach(() => {
    vi.unstubAllGlobals();
    // Successful summaries are cached for the session, so one test's would otherwise answer the
    // next one's fetch.
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
        const location = stubLocationHref();

        try {
            render(<Provider store={store}><AccessProvider><SummaryPage /></AccessProvider></Provider>);
            // The access summary fetch fails (fetchMock throws for every URL) and fails open,
            // so the page renders once that resolves.
            await waitFor(() => expect(document.querySelector('antrea-summary-page')).not.toBeNull());
            const el = document.querySelector('antrea-summary-page')!;

            await act(async () => {
                el.dispatchEvent(new CustomEvent('antrea-session-expired'));
            });

            await waitFor(() => expect(store.getState().session).toBe('anonymous'));
            expect(location.hrefSetter).toHaveBeenCalledTimes(1);
            const redirect = location.hrefSetter.mock.calls[0][0] as string;
            expect(redirect).toContain('/auth/logout?');
            // The message is nested inside the redirect_url parameter, hence double-encoded.
            expect(decodeURIComponent(redirect)).toContain('session+has+expired');
            // No /auth/* round-trip: the old code tried a token refresh here first.
            expect(fetchMock.mock.calls.filter(([url]) => url.startsWith('/auth/'))).toHaveLength(0);
        } finally {
            location.restore();
        }
    });
});

// The route guard, as opposed to the nav entry. nav.test.tsx covers the entry, but it mocks
// useCanViewFlows wholesale, so nothing there connects the two — and the entry is not the
// enforcement point anyway: a user who bookmarked /flows/list or typed it never goes near the nav.
// These use the real hook through AccessProvider, so they also pin that the page and the nav read
// the same rule rather than two that happen to agree today.
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

    function renderPage(summary: AccessSummary, sessionInfo: RootState['sessionInfo']) {
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(
            new Response(JSON.stringify(summary), { status: 200 })));
        const store = setupStore({ session: 'authenticated', sessionInfo });
        return render(
            <Provider store={store}>
                <AccessProvider><FlowVisibilityPage view="list" /></AccessProvider>
            </Provider>,
        );
    }

    const tokenSession = { authenticated: true, mode: 'token' as const, username: 'alice' };

    test('renders the permission panel, and never the page, for a denied user', async () => {
        renderPage(summaryWith({ clusterAdmin: false }), tokenSession);
        await waitFor(() => expect(document.querySelector('antrea-alert')).not.toBeNull());
        expect(document.querySelector('antrea-alert')!.textContent)
            .toContain('You do not have permission to view this page');
        // The point of the guard: the Lit element is never constructed, so it never opens the
        // stream. Asserting the panel alone would pass with both rendered.
        expect(document.querySelector('antrea-flow-visibility-page')).toBeNull();
    });

    test('renders the page for a cluster admin', async () => {
        renderPage(summaryWith({ clusterAdmin: true }), tokenSession);
        await waitFor(() => expect(document.querySelector('antrea-flow-visibility-page')).not.toBeNull());
        expect(document.querySelector('antrea-alert')).toBeNull();
    });
});
