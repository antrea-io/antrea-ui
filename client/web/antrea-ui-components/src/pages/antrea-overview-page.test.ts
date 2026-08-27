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

import { afterEach, describe, expect, test, vi } from 'vitest';
import './antrea-overview-page';
import type { AntreaOverviewPage } from './antrea-overview-page';
import { resetAccessSummary } from '../lib/access-api';
import type { AccessSummary } from '../lib/access-api';

function jsonResponse(body: unknown, status = 200): Response {
    return new Response(JSON.stringify(body), { status });
}

function item(name: string, namespace?: string) {
    return { metadata: namespace ? { name, namespace } : { name } };
}

function list(names: string[], namespace?: string) {
    return { items: names.map(n => item(n, namespace)) };
}

function fullAccessSummary(): AccessSummary {
    return {
        username: 'alice',
        groups: [],
        clusterAdmin: true,
        rules: {
            resourceRules: [{ apiGroups: ['*'], resources: ['*'], verbs: ['*'] }],
            nonResourceRules: [{ nonResourceURLs: ['*'], verbs: ['*'] }],
            incomplete: false,
        },
        namespaces: ['*'],
    };
}

// Maps a request URL back to the RESOURCES key it corresponds to (see antrea-overview-page.ts),
// so tests can stub responses by resource name instead of by exact k8s proxy path.
function keyForUrl(rawUrl: string): string | null {
    // Every list request carries a ?limit= (see withPageLimit in antrea-overview-page.ts); match
    // on the path alone so these stay path assertions.
    const url = rawUrl.split('?')[0];
    if (url.endsWith('/access-summary')) return 'access-summary';
    if (url.endsWith('/api/v1/namespaces')) return 'namespaces';
    if (url.includes('/networking.k8s.io/v1/') && url.endsWith('/networkpolicies')) return 'k8sNetworkPolicies';
    if (url.includes('/crd.antrea.io/v1beta1/') && url.endsWith('/clusternetworkpolicies')) return 'antreaClusterNetworkPolicies';
    if (url.includes('/crd.antrea.io/v1beta1/') && url.endsWith('/networkpolicies')) return 'antreaNetworkPolicies';
    if (url.endsWith('/pods')) return 'pods';
    if (url.endsWith('/services')) return 'services';
    if (url.endsWith('/deployments')) return 'deployments';
    if (url.endsWith('/statefulsets')) return 'statefulsets';
    if (url.endsWith('/daemonsets')) return 'daemonsets';
    return null;
}

let el: AntreaOverviewPage | undefined;

afterEach(() => {
    el?.remove();
    el = undefined;
    vi.unstubAllGlobals();
    resetAccessSummary();
});

/** Mounts the page against an arbitrary fetch handler, for cases mount()'s by-resource-key
 * stubbing cannot express (per-URL status codes, namespace-scoped path assertions). */
async function mountWith(handler: (url: string) => Promise<Response>): Promise<AntreaOverviewPage> {
    vi.stubGlobal('fetch', vi.fn(handler));
    el = document.createElement('antrea-overview-page') as AntreaOverviewPage;
    document.body.appendChild(el);
    await el.updateComplete;
    await new Promise(r => setTimeout(r, 0));
    await new Promise(r => setTimeout(r, 0));
    await el.updateComplete;
    return el;
}

async function mount(
    overrides: Partial<Record<string, unknown>> = {},
    summary: AccessSummary | null = fullAccessSummary(),
): Promise<AntreaOverviewPage> {
    return mountWith(async (url: string) => {
        const key = keyForUrl(url);
        if (key === 'access-summary') {
            return summary === null ? new Response('', { status: 500 }) : jsonResponse(summary);
        }
        if (key && key in overrides) return jsonResponse(overrides[key]);
        if (key) return jsonResponse({ items: [] });
        throw new Error(`unexpected fetch to ${url}`);
    });
}

function tileValue(page: AntreaOverviewPage, heading: string): string | null {
    const value = page.shadowRoot!.querySelector(`antrea-card[heading="${heading}"] .stat-value`);
    return value ? (value.textContent ?? '').trim() : null;
}

function hasTile(page: AntreaOverviewPage, heading: string): boolean {
    return page.shadowRoot!.querySelector(`antrea-card[heading="${heading}"]`) !== null;
}

function alertText(page: AntreaOverviewPage, status: 'info' | 'danger'): string | null {
    const alert = page.shadowRoot!.querySelector(`antrea-alert[status="${status}"]`);
    return alert === null ? null : (alert.textContent ?? '').trim();
}

function listRows(page: AntreaOverviewPage, heading: string): string[][] {
    const table = page.shadowRoot!.querySelector(`antrea-card[heading="${heading}"] table`);
    return Array.from(table?.querySelectorAll('tbody tr') ?? []).map(
        row => Array.from(row.querySelectorAll('td')).map(cell => cell.textContent ?? ''),
    );
}

describe('AntreaOverviewPage — tiles', () => {
    test('renders a count per resource type', async () => {
        const page = await mount({
            namespaces: list(['default', 'kube-system']),
            pods: list(['p1', 'p2', 'p3'], 'default'),
            services: list(['svc1'], 'default'),
        });

        expect(tileValue(page, 'Namespaces')).toBe('2');
        expect(tileValue(page, 'Pods')).toBe('3');
        expect(tileValue(page, 'Services')).toBe('1');
        expect(tileValue(page, 'Deployments')).toBe('0');
    });

    test('combines Antrea ClusterNetworkPolicy and NetworkPolicy counts into one tile', async () => {
        const page = await mount({
            antreaClusterNetworkPolicies: list(['acnp1']),
            antreaNetworkPolicies: list(['anp1', 'anp2'], 'default'),
        });

        expect(tileValue(page, 'Antrea Network Policies')).toBe('3');
    });

    test('a truncated list (metadata.continue present) shows a "+" instead of a false-exact count', async () => {
        const page = await mount({
            pods: { items: [item('p1', 'default')], metadata: { continue: 'abc' } },
        });

        expect(tileValue(page, 'Pods')).toBe('1+');
    });

    test('a truncated list reporting remainingItemCount shows the true total, not a floor', async () => {
        const page = await mount({
            pods: { items: [item('p1', 'default')], metadata: { continue: 'abc', remainingItemCount: 41 } },
        });

        expect(tileValue(page, 'Pods')).toBe('42');
    });

    test('every list request carries a page limit', async () => {
        const seenUrls: string[] = [];
        await mountWith(async (url: string) => {
            seenUrls.push(url);
            const key = keyForUrl(url);
            if (key === 'access-summary') return jsonResponse(fullAccessSummary());
            if (key) return jsonResponse({ items: [] });
            throw new Error(`unexpected fetch to ${url}`);
        });

        // An unlimited list would pull every Pod spec in the cluster through the proxy just to
        // count them, and would never set metadata.continue, making the truncation handling dead.
        const listUrls = seenUrls.filter(u => keyForUrl(u) !== 'access-summary');
        expect(listUrls.length).toBeGreaterThan(0);
        for (const url of listUrls) {
            expect(new URL(url, 'http://localhost').searchParams.get('limit')).toBe('500');
        }
    });
});

describe('AntreaOverviewPage — per-resource degradation', () => {
    test('a resource the user cannot list is skipped, not fetched, and a note is shown', async () => {
        const summary: AccessSummary = {
            ...fullAccessSummary(),
            rules: {
                resourceRules: [{ apiGroups: [''], resources: ['pods'], verbs: ['list'] }],
                nonResourceRules: [],
                incomplete: false,
            },
        };
        const page = await mount({ pods: list(['p1'], 'default') }, summary);

        expect(hasTile(page, 'Pods')).toBe(true);
        expect(hasTile(page, 'Services')).toBe(false);
        expect(alertText(page, 'info')).not.toBeNull();
    });

    test('one resource 403ing degrades that tile while the others still render', async () => {
        const page = await mountWith(async (url: string) => {
            const key = keyForUrl(url);
            if (key === 'access-summary') return jsonResponse(fullAccessSummary());
            if (key === 'pods') return new Response('forbidden', { status: 403 });
            if (key) return jsonResponse({ items: [] });
            throw new Error(`unexpected fetch to ${url}`);
        });

        expect(hasTile(page, 'Pods')).toBe(false);
        expect(hasTile(page, 'Services')).toBe(true);
        expect(alertText(page, 'info')).not.toBeNull();
        // A 403 is a permissions problem, not a failure: no danger alert.
        expect(alertText(page, 'danger')).toBeNull();
    });

    test('access-summary fetch failure fails open: every resource is fetched', async () => {
        const page = await mount({ pods: list(['p1'], 'default') }, null);

        expect(hasTile(page, 'Pods')).toBe(true);
        expect(hasTile(page, 'Services')).toBe(true);
        expect(alertText(page, 'info')).toBeNull();
    });

    test('a non-403 failure shows a danger alert carrying the error', async () => {
        const page = await mountWith(async (url: string) => {
            const key = keyForUrl(url);
            if (key === 'access-summary') return jsonResponse(fullAccessSummary());
            if (key === 'pods') return new Response('backend exploded', { status: 500 });
            if (key) return jsonResponse({ items: [] });
            throw new Error(`unexpected fetch to ${url}`);
        });

        expect(alertText(page, 'danger')).toContain('backend exploded');
    });

    test('a 401 response dispatches antrea-session-expired', async () => {
        vi.stubGlobal('fetch', vi.fn(async () => new Response('', { status: 401 })));
        el = document.createElement('antrea-overview-page') as AntreaOverviewPage;
        const onSessionExpired = vi.fn();
        el.addEventListener('antrea-session-expired', onSessionExpired);
        document.body.appendChild(el);
        await el.updateComplete;
        await new Promise(r => setTimeout(r, 0));
        await new Promise(r => setTimeout(r, 0));

        expect(onSessionExpired).toHaveBeenCalledTimes(1);
    });
});

describe('AntreaOverviewPage — namespace filter', () => {
    test('selecting a namespace re-fetches namespaced resources scoped to it', async () => {
        const page = await mount({
            namespaces: list(['default', 'kube-system']),
            pods: list(['p1', 'p2'], 'default'),
        });
        expect(tileValue(page, 'Pods')).toBe('2');

        const seenUrls: string[] = [];
        vi.stubGlobal('fetch', vi.fn(async (url: string) => {
            seenUrls.push(url);
            const key = keyForUrl(url);
            if (key === 'access-summary') return jsonResponse(fullAccessSummary());
            if (key === 'pods') return jsonResponse(list(['p1'], 'kube-system'));
            return jsonResponse({ items: [] });
        }));

        const select = page.shadowRoot!.querySelector('#ns-select') as HTMLSelectElement;
        select.value = 'kube-system';
        select.dispatchEvent(new Event('change'));
        await page.updateComplete;
        await new Promise(r => setTimeout(r, 0));
        await new Promise(r => setTimeout(r, 0));
        await page.updateComplete;

        expect(tileValue(page, 'Pods')).toBe('1');
        expect(seenUrls.some(u => u.split('?')[0].endsWith('/api/v1/namespaces/kube-system/pods'))).toBe(true);
    });

    test('the picked namespace stays selected after the reload it triggers', async () => {
        const page = await mount({
            namespaces: list(['default', 'kube-system']),
            pods: list(['p1', 'p2'], 'default'),
        });

        // The reload must be slow enough for at least one render to land while it is in flight.
        // With instantly-resolved mocks Lit coalesces the whole reload into a single update, so
        // the intermediate state this test is about never reaches the DOM.
        vi.stubGlobal('fetch', vi.fn(async (url: string) => {
            const key = keyForUrl(url);
            await new Promise(r => setTimeout(r, 5));
            if (key === 'access-summary') return jsonResponse(fullAccessSummary());
            if (key === 'namespaces') return jsonResponse(list(['default', 'kube-system']));
            return jsonResponse({ items: [] });
        }));

        const select = page.shadowRoot!.querySelector('#ns-select') as HTMLSelectElement;
        select.value = 'kube-system';
        select.dispatchEvent(new Event('change'));

        // Render once mid-flight: this is where the old code swapped the page for a spinner.
        await page.updateComplete;
        for (let i = 0; i < 10; i++) await new Promise(r => setTimeout(r, 5));
        await page.updateComplete;

        // Regression: the reload used to blank the page, so the <select> was torn down and
        // rebuilt, and its value was assigned before its options existed — silently snapping the
        // selection back to "All Namespaces" while the counts stayed namespace-scoped.
        const after = page.shadowRoot!.querySelector('#ns-select') as HTMLSelectElement;
        expect(after).not.toBeNull();
        expect(after.value).toBe('kube-system');
    });
});

describe('AntreaOverviewPage — click-through to Service Map', () => {
    test('clicking a Pod row dispatches antrea-navigate with a pod+namespace filter', async () => {
        const page = await mount({ pods: list(['web-1'], 'default') });
        const onNavigate = vi.fn();
        page.addEventListener('antrea-navigate', onNavigate);

        expect(listRows(page, 'Pods')).toEqual([['default', 'web-1']]);
        const row = page.shadowRoot!.querySelector('antrea-card[heading="Pods"] tbody tr') as HTMLElement;
        row.click();

        expect(onNavigate).toHaveBeenCalledTimes(1);
        const detail = (onNavigate.mock.calls[0][0] as CustomEvent).detail;
        expect(detail).toEqual({ path: '/flows', search: { view: 'map', namespaces: 'default', pods: 'web-1' } });
    });

    test('clicking a Service row dispatches antrea-navigate with a service+namespace filter', async () => {
        const page = await mount({ services: list(['svc1'], 'default') });
        const onNavigate = vi.fn();
        page.addEventListener('antrea-navigate', onNavigate);

        const row = page.shadowRoot!.querySelector('antrea-card[heading="Services"] tbody tr') as HTMLElement;
        row.click();

        expect(onNavigate).toHaveBeenCalledTimes(1);
        const detail = (onNavigate.mock.calls[0][0] as CustomEvent).detail;
        // "namespace/name": a bare service name is dropped by destinationK8sServiceFilterKey on
        // the Flow Visibility side, so the deep link has to carry the qualified form.
        expect(detail).toEqual({ path: '/flows', search: { view: 'map', namespaces: 'default', services: 'default/svc1' } });
    });
});
