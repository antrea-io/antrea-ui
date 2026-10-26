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
import type { AntreaNamespacePicker } from '../antrea-namespace-picker';
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
    if (url.endsWith('/access-summary/namespaces')) return 'access-summary-namespaces';
    if (url.endsWith('/api/v1/namespaces')) return 'namespaces';
    if (url.includes('/networking.k8s.io/v1/') && url.endsWith('/networkpolicies')) return 'k8sNetworkPolicies';
    if (url.includes('/crd.antrea.io/v1beta1/') && url.endsWith('/clusternetworkpolicies')) return 'antreaClusterNetworkPolicies';
    if (url.includes('/crd.antrea.io/v1beta1/') && url.endsWith('/networkpolicies')) return 'antreaNetworkPolicies';
    if (url.endsWith('/pods')) return 'pods';
    if (url.endsWith('/services')) return 'services';
    if (url.endsWith('/deployments')) return 'deployments';
    if (url.endsWith('/statefulsets')) return 'statefulsets';
    if (url.endsWith('/daemonsets')) return 'daemonsets';
    if (url.endsWith('/events')) return 'events';
    return null;
}

function event(overrides: {
    kind?: string; namespace?: string; name?: string; reason?: string; message?: string;
    type?: string; lastTimestamp?: string;
} = {}) {
    return {
        metadata: { name: overrides.name ?? 'ev1', namespace: overrides.namespace },
        involvedObject: { kind: overrides.kind ?? 'Pod', namespace: overrides.namespace, name: overrides.name },
        reason: overrides.reason ?? 'Started',
        message: overrides.message ?? 'Started container',
        type: overrides.type ?? 'Normal',
        lastTimestamp: overrides.lastTimestamp ?? '2026-01-01T00:00:00Z',
    };
}

function list1(single: unknown) {
    return { items: [single] };
}

// The picker's value for All Namespaces.
const ALL = '__cluster_wide__';

let el: AntreaOverviewPage | undefined;

afterEach(() => {
    el?.remove();
    el = undefined;
    vi.unstubAllGlobals();
    resetAccessSummary();
    window.history.replaceState(null, '', '/');
});

/** Mounts the page against an arbitrary fetch handler, for cases mount()'s by-resource-key
 * stubbing cannot express (per-URL status codes, namespace-scoped path assertions). */
async function mountWith(handler: (url: string) => Promise<Response>, search?: string): Promise<AntreaOverviewPage> {
    vi.stubGlobal('fetch', vi.fn(handler));
    el = document.createElement('antrea-overview-page') as AntreaOverviewPage;
    if (search !== undefined) el.locationSearch = search;
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
    search?: string,
): Promise<AntreaOverviewPage> {
    return mountWith(async (url: string) => {
        const key = keyForUrl(url);
        if (key === 'access-summary') {
            return summary === null ? new Response('', { status: 500 }) : jsonResponse(summary);
        }
        if (key && key in overrides) return jsonResponse(overrides[key]);
        if (key) return jsonResponse({ items: [] });
        throw new Error(`unexpected fetch to ${url}`);
    }, search);
}

async function settle(page: AntreaOverviewPage) {
    await page.updateComplete;
    await new Promise(r => setTimeout(r, 0));
    await new Promise(r => setTimeout(r, 0));
    await page.updateComplete;
}

function picker(page: AntreaOverviewPage): AntreaNamespacePicker {
    return page.shadowRoot!.querySelector('antrea-namespace-picker') as AntreaNamespacePicker;
}

/** The scope the picker shows as selected: '__cluster_wide__' for All Namespaces, '' for none. */
function pickerValue(page: AntreaOverviewPage): string {
    return (page.shadowRoot!.querySelector('#ns-picker') as HTMLInputElement).dataset.value ?? '';
}

/** The values of the open menu's options, in order. */
function menuValues(page: AntreaOverviewPage): string[] {
    return Array.from(page.shadowRoot!.querySelectorAll('#ns-picker-list [role="option"]'))
        .map(o => (o as HTMLElement).dataset.value!);
}

/** What the picker offers with nothing typed: opens the menu, reads it, and closes it. */
async function optionValues(page: AntreaOverviewPage): Promise<string[]> {
    const input = page.shadowRoot!.querySelector('#ns-picker') as HTMLInputElement;
    input.dispatchEvent(new Event('focus'));
    await picker(page).updateComplete;
    const values = menuValues(page);
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await picker(page).updateComplete;
    return values;
}

/** Picks a scope in the picker, as a user does, and waits for the page to load it. */
async function choose(page: AntreaOverviewPage, value: string) {
    const input = page.shadowRoot!.querySelector('#ns-picker') as HTMLInputElement;
    input.dispatchEvent(new Event('focus'));
    await picker(page).updateComplete;
    (page.shadowRoot!.querySelector(`#ns-picker-list [data-value="${value}"]`) as HTMLElement).click();
    await settle(page);
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

    test('Antrea ClusterNetworkPolicies and NetworkPolicies are separate tiles', async () => {
        const page = await mount({
            antreaClusterNetworkPolicies: list(['acnp1']),
            antreaNetworkPolicies: list(['anp1', 'anp2'], 'default'),
        });

        expect(tileValue(page, 'Antrea Network Policies')).toBe('2');
        expect(tileValue(page, 'Antrea Cluster Network Policies')).toBe('1');
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

    test('every list request carries a page limit sized to what it is used for', async () => {
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
        // A tile only needs a count, which a small page gives exactly (items plus the remainder);
        // the event rows are built from the page itself. The picker's list is the one request that
        // is not a tile's, and its limit is the safeguard of the list (namespace-access.ts).
        const listUrls = seenUrls.filter(u => keyForUrl(u) !== 'access-summary');
        expect(listUrls.length).toBeGreaterThan(0);
        for (const url of listUrls) {
            const limit = new URL(url, 'http://localhost').searchParams.get('limit');
            const expected = keyForUrl(url) === 'events' ? ['2000']
                : keyForUrl(url) === 'namespaces' ? ['100', '10000'] : ['100'];
            expect([url, expected.includes(limit!)]).toEqual([url, true]);
        }
    });
});

describe('AntreaOverviewPage — resource labels', () => {
    // One spelling per resource: the tile heading and the error message are the same string.
    test('an error message names a resource as its tile does', async () => {
        const page = await mountWith(async (url: string) => {
            const key = keyForUrl(url);
            if (key === 'access-summary') return jsonResponse(fullAccessSummary());
            if (key === 'antreaNetworkPolicies') return jsonResponse({ message: 'boom' }, 500);
            return jsonResponse({ items: [] });
        });
        expect(hasTile(page, 'Antrea Network Policies')).toBe(false);
        expect(alertText(page, 'danger')).toContain('Antrea Network Policies:');
    });

    test('shows the tiles in a fixed order', async () => {
        const page = await mount({});
        const headings = Array.from(page.shadowRoot!.querySelectorAll('.stat-grid antrea-card'))
            .map(c => c.getAttribute('heading'));
        expect(headings).toEqual([
            'Namespaces', 'Pods', 'Services', 'Deployments', 'StatefulSets', 'DaemonSets',
            'K8s Network Policies', 'Antrea Network Policies', 'Antrea Cluster Network Policies',
        ]);
    });
});

describe('AntreaOverviewPage — per-resource degradation', () => {
    test('a resource the user cannot list is skipped, not fetched, and a note is shown', async () => {
        const summary: AccessSummary = {
            ...fullAccessSummary(),
            clusterAdmin: false,
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

    // A ServiceAccount that can read workloads but not NetworkPolicies is a realistic split
    // (workload-reader roles rarely also grant policy visibility), and exercises the mix of
    // core/v1, apps/v1 and two different NetworkPolicy gates together, rather than the single
    // resource the test above denies.
    test('a SA that can list workloads but not NetworkPolicies sees workload tiles only', async () => {
        const summary: AccessSummary = {
            ...fullAccessSummary(),
            clusterAdmin: false,
            rules: {
                resourceRules: [
                    { apiGroups: ['apps'], resources: ['deployments', 'statefulsets', 'daemonsets'], verbs: ['list'] },
                ],
                nonResourceRules: [],
                incomplete: false,
            },
        };
        const page = await mount({
            deployments: list(['d1'], 'default'),
            statefulsets: list(['s1'], 'default'),
            daemonsets: list(['ds1'], 'default'),
        }, summary);

        expect(tileValue(page, 'Deployments')).toBe('1');
        expect(tileValue(page, 'StatefulSets')).toBe('1');
        expect(tileValue(page, 'DaemonSets')).toBe('1');
        expect(hasTile(page, 'K8s Network Policies')).toBe(false);
        expect(hasTile(page, 'Antrea Network Policies')).toBe(false);
        expect(hasTile(page, 'Pods')).toBe(false);
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

    // Rule 9 of the namespace scope design: the page still works, with All Namespaces offered and
    // a way to name a namespace, and the API's answer decides what is shown.
    test('access-summary fetch failure fails open: every resource is fetched once All Namespaces is picked', async () => {
        const page = await mount({ pods: list(['p1'], 'default') }, null);
        expect(hasTile(page, 'Pods')).toBe(false);

        await choose(page, ALL);
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

describe('AntreaOverviewPage — namespace scope', () => {
    const rulesFor = (...rules: { apiGroups: string[], resources: string[], verbs: string[] }[]) =>
        ({ resourceRules: rules, nonResourceRules: [], incomplete: false });
    const podsList = { apiGroups: [''], resources: ['pods'], verbs: ['list'] };

    /** A summary that grants nothing cluster-wide, with the namespaces RoleBindings name. */
    function boundSummary(namespaces: string[], incomplete = false) {
        return {
            username: 'bob', groups: [], clusterAdmin: false, namespaces,
            rules: { resourceRules: [], nonResourceRules: [], incomplete },
        };
    }

    /** Serves the summaries of a user whose pods grant is in the namespaces of `grants`. */
    function boundHandler(namespaces: string[], grants: string[], seen: string[] = [], incomplete = false) {
        return async (url: string) => {
            seen.push(url);
            const key = keyForUrl(url);
            const u = new URL(url, 'http://x');
            if (key === 'access-summary') return jsonResponse(boundSummary(namespaces, incomplete));
            if (key === 'access-summary-namespaces') {
                return jsonResponse({
                    items: u.searchParams.getAll('namespace').map(namespace => ({
                        namespace, rules: grants.includes(namespace) ? rulesFor(podsList) : rulesFor(),
                    })),
                });
            }
            if (key === 'pods') return jsonResponse(list(['p1', 'p2'], 'team-a'));
            return jsonResponse({ items: [] });
        };
    }

    describe('the default scope', () => {
        test('is All Namespaces for a user whose verdict is allowed', async () => {
            const page = await mount({ namespaces: list(['default', 'kube-system']) });
            expect(pickerValue(page)).toBe(ALL);
            expect(await optionValues(page)).toEqual([ALL, 'default', 'kube-system']);
        });

        test('builds the list when the page loads, not from the Namespaces tile', async () => {
            const seen: string[] = [];
            await mountWith(async (url: string) => {
                seen.push(url);
                const key = keyForUrl(url);
                if (key === 'access-summary') return jsonResponse(fullAccessSummary());
                if (key === 'namespaces') return jsonResponse(list(['a', 'b']));
                return jsonResponse({ items: [] });
            });
            expect(seen.some(u => u.includes('/api/v1/namespaces?limit=10000'))).toBe(true);
        });

        test('is the first namespace, unchecked, when the verdict is not allowed', async () => {
            const seen: string[] = [];
            const page = await mountWith(boundHandler(['team-a', 'team-b'], ['team-b'], seen));
            expect(pickerValue(page)).toBe('team-a');
            // Only the selected namespace is asked about: the choice was not made by checking.
            const asked = seen.filter(u => keyForUrl(u) === 'access-summary-namespaces')
                .map(u => new URL(u, 'http://x').searchParams.getAll('namespace'));
            expect(asked).toEqual([['team-a']]);
        });

        test('is not All Namespaces when the verdict is unknown', async () => {
            const page = await mountWith(boundHandler(['team-a', 'team-b'], [], [], true));
            expect(pickerValue(page)).toBe('team-a');
            // Offered, though: only a denied verdict hides it.
            expect(await optionValues(page)).toEqual([ALL, 'team-a', 'team-b']);
        });

        test('offers the list of a user who cannot list namespaces as it is, whatever rules.incomplete says', async () => {
            const page = await mountWith(boundHandler(['team-a', 'team-b'], [], [], true));
            expect(await optionValues(page)).toEqual([ALL, 'team-a', 'team-b']);
        });

        test('leaves All Namespaces out when the verdict is denied', async () => {
            const page = await mountWith(boundHandler(['team-a', 'team-b'], []));
            expect(await optionValues(page)).toEqual(['team-a', 'team-b']);
        });

        test('is nothing, with an explanation and a box to name one, when there is no list', async () => {
            const page = await mount({}, null);
            expect(pickerValue(page)).toBe('');
            expect(hasTile(page, 'Pods')).toBe(false);
            expect(page.shadowRoot!.querySelector('.stat-grid')).toBeNull();
            expect(page.shadowRoot!.textContent).toContain('Select a namespace');
            expect(page.shadowRoot!.querySelector('#ns-picker-by-name')).not.toBeNull();
            // All Namespaces is still offered when the summary could not be fetched.
            expect(await optionValues(page)).toEqual([ALL]);
        });

        test('is nothing for a user with an empty list, who may name a namespace', async () => {
            const page = await mountWith(boundHandler([], []));
            expect(pickerValue(page)).toBe('');
            expect(page.shadowRoot!.querySelector('#ns-picker-by-name')).not.toBeNull();
        });

        test('is not written to the URL, and is not a choice', async () => {
            const chosen: string[] = [];
            vi.stubGlobal('fetch', vi.fn(boundHandler(['team-a'], ['team-a'])));
            el = document.createElement('antrea-overview-page') as AntreaOverviewPage;
            el.addEventListener('antrea-namespace-scope-chosen', e => chosen.push((e as CustomEvent).detail.scope));
            document.body.appendChild(el);
            await settle(el);
            expect(pickerValue(el)).toBe('team-a');
            expect(window.location.search).toBe('');
            expect(chosen).toEqual([]);
        });
    });

    describe('the scope in the URL', () => {
        test('is used as given, without waiting for the list', async () => {
            const seen: string[] = [];
            const listGate = Promise.withResolvers<void>();
            const page = await mountWith(async (url: string) => {
                seen.push(url);
                if (keyForUrl(url) === 'access-summary') await listGate.promise;
                return boundHandler(['team-a'], ['team-b'])(url);
            }, '?namespace=team-b');
            // The summary has not answered, yet team-b's content is already being requested.
            expect(seen.some(u => u.split('?')[0].endsWith('/namespaces/team-b/pods'))
                || seen.some(u => u.includes('access-summary/namespaces?namespace=team-b'))).toBe(true);
            expect(pickerValue(page)).toBe('team-b');
            listGate.resolve();
        });

        test('is kept when it is not in the list, and the picker shows it', async () => {
            const page = await mountWith(boundHandler(['team-a'], ['team-b']), '?namespace=team-b');
            expect(pickerValue(page)).toBe('team-b');
            expect(await optionValues(page)).toContain('team-b');
        });

        test('keeps All Namespaces on a page whose verdict is denied, with no fallback', async () => {
            const seen: string[] = [];
            const page = await mountWith(boundHandler(['team-a'], [], seen), '?namespace=_all');
            expect(pickerValue(page)).toBe(ALL);
            expect(await optionValues(page)).toEqual([ALL, 'team-a']);
            // The page's verdict is denied, so the pods tile is left out without a request (rule 6
            // of the design), but the scope stays, as the URL gave it.
            expect(seen.some(u => keyForUrl(u) === 'pods' && u.includes('/api/v1/pods'))).toBe(false);
            expect(page.shadowRoot!.textContent).toContain('does not have permission to view any');
        });

        test('is reported, with nothing selected, when it is not a valid scope', async () => {
            for (const search of ['?namespace=', '?namespace=Not_Valid', '?namespace=a&namespace=b']) {
                const page = await mountWith(boundHandler(['team-a'], ['team-a']), search);
                expect(pickerValue(page)).toBe('');
                expect(alertText(page, 'danger')).not.toBeNull();
                expect(hasTile(page, 'Pods')).toBe(false);
                page.remove();
            }
        });

        test('is taken again when the URL changes', async () => {
            const page = await mountWith(boundHandler(['team-a', 'team-b'], ['team-a', 'team-b']), '?namespace=team-a');
            expect(pickerValue(page)).toBe('team-a');
            page.locationSearch = '?namespace=team-b';
            await settle(page);
            expect(pickerValue(page)).toBe('team-b');
            // No scope in the new URL: the default order runs again.
            page.locationSearch = '';
            await settle(page);
            expect(pickerValue(page)).toBe('team-a');
        });

        test('is said to the host when taken from the URL', async () => {
            const chosen: string[] = [];
            vi.stubGlobal('fetch', vi.fn(boundHandler(['team-a'], ['team-a'])));
            el = document.createElement('antrea-overview-page') as AntreaOverviewPage;
            el.locationSearch = '?namespace=_all';
            el.addEventListener('antrea-namespace-scope-chosen', e => chosen.push((e as CustomEvent).detail.scope));
            document.body.appendChild(el);
            await settle(el);
            expect(chosen).toContain('_all');
        });
    });

    describe('choosing a scope', () => {
        test('re-fetches namespaced resources scoped to it', async () => {
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
            await choose(page, 'kube-system');

            expect(tileValue(page, 'Pods')).toBe('1');
            expect(seenUrls.some(u => u.split('?')[0].endsWith('/api/v1/namespaces/kube-system/pods'))).toBe(true);
        });

        test('the picked namespace stays selected after the reload it triggers', async () => {
            const page = await mount({
                namespaces: list(['default', 'kube-system']),
                pods: list(['p1', 'p2'], 'default'),
            });
            // The reload must be slow enough for at least one render to land while it is in flight.
            vi.stubGlobal('fetch', vi.fn(async (url: string) => {
                const key = keyForUrl(url);
                await new Promise(r => setTimeout(r, 5));
                if (key === 'access-summary') return jsonResponse(fullAccessSummary());
                return jsonResponse({ items: [] });
            }));

            const input = page.shadowRoot!.querySelector('#ns-picker') as HTMLInputElement;
            input.dispatchEvent(new Event('focus'));
            await picker(page).updateComplete;
            (page.shadowRoot!.querySelector('#ns-picker-list [data-value="kube-system"]') as HTMLElement).click();
            await page.updateComplete;
            for (let i = 0; i < 10; i++) await new Promise(r => setTimeout(r, 5));
            await page.updateComplete;

            const after = page.shadowRoot!.querySelector('#ns-picker') as HTMLInputElement;
            expect(after).not.toBeNull();
            expect(after.dataset.value).toBe('kube-system');
        });

        test('is written to the URL in place, and said to the host', async () => {
            const chosen: string[] = [];
            const changes: string[] = [];
            vi.stubGlobal('fetch', vi.fn(boundHandler(['team-a', 'team-b'], ['team-a', 'team-b'])));
            el = document.createElement('antrea-overview-page') as AntreaOverviewPage;
            el.addEventListener('antrea-namespace-scope-chosen', e => chosen.push((e as CustomEvent).detail.scope));
            el.addEventListener('antrea-namespace-scope-change', e => changes.push((e as CustomEvent).detail.search));
            document.body.appendChild(el);
            await settle(el);
            const before = window.history.length;

            await choose(el, 'team-b');
            expect(window.location.search).toBe('?namespace=team-b');
            expect(window.history.length).toBe(before);
            expect(chosen).toEqual(['team-b']);
            expect(changes).toEqual(['?namespace=team-b']);

            // Chosen again, though it is the one in force: the choice is written all the same.
            await choose(el, 'team-b');
            expect(window.location.search).toBe('?namespace=team-b');
        });

        test('typed text searches the list by prefix, ignoring case, and hides All Namespaces', async () => {
            const names = Array.from({ length: 25 }, (_, i) => `ns-${String(i).padStart(2, '0')}`);
            const page = await mount({ namespaces: list([...names, 'other']) });

            const input = page.shadowRoot!.querySelector('#ns-picker') as HTMLInputElement;
            input.dispatchEvent(new Event('focus'));
            await picker(page).updateComplete;
            // Nothing typed: All Namespaces first, then the first ten.
            expect(menuValues(page)).toEqual([ALL, ...names.slice(0, 10)]);
            expect(page.shadowRoot!.textContent).toContain('type to search');

            input.value = ' NS-1 ';
            input.dispatchEvent(new Event('input'));
            await picker(page).updateComplete;
            expect(menuValues(page)).toEqual(names.slice(10, 20));

            input.value = 'oth';
            input.dispatchEvent(new Event('input'));
            await picker(page).updateComplete;
            expect(menuValues(page)).toEqual(['other']);
        });

        test('Enter on a typed name picks it', async () => {
            const page = await mount({ namespaces: list(['default', 'kube-system']) });
            const input = page.shadowRoot!.querySelector('#ns-picker') as HTMLInputElement;
            input.dispatchEvent(new Event('focus'));
            input.value = 'kube';
            input.dispatchEvent(new Event('input'));
            input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }));
            await settle(page);
            expect(pickerValue(page)).toBe('kube-system');
        });

        test('a typed name not in a complete list cannot be picked', async () => {
            const page = await mount({ namespaces: list(['default', 'kube-system']) });
            const input = page.shadowRoot!.querySelector('#ns-picker') as HTMLInputElement;
            input.dispatchEvent(new Event('focus'));
            input.value = 'nope';
            input.dispatchEvent(new Event('input'));
            input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }));
            await settle(page);
            expect(pickerValue(page)).toBe(ALL);
        });

        test('a name typed in the box is taken, and rejected when it is not a valid name', async () => {
            const page = await mount({}, null);
            const byName = page.shadowRoot!.querySelector('#ns-picker-by-name') as HTMLInputElement;
            byName.value = 'Not_Valid';
            byName.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }));
            await settle(page);
            expect(pickerValue(page)).toBe('');
            expect(alertText(page, 'danger')).toContain('not a valid namespace name');

            byName.value = 'team-a';
            byName.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }));
            await settle(page);
            expect(pickerValue(page)).toBe('team-a');
            expect(alertText(page, 'danger')).toBeNull();
            expect(tileValue(page, 'Pods')).not.toBeNull();
        });
    });

    describe('the note on a Namespace outside the list', () => {
        const note = (page: AntreaOverviewPage) =>
            page.shadowRoot!.querySelector('[data-testid="scope-note"]')?.textContent?.trim() ?? null;

        test('says it is not in your list for a user who cannot list namespaces', async () => {
            const page = await mountWith(boundHandler(['team-a'], ['team-b']), '?namespace=team-b');
            expect(note(page)).toBe('Namespace team-b is not in your list: it may not exist, or you may not have access');
        });

        // The per-Namespace access summary does not check existence: a Namespace that is not there
        // has no RoleBindings, so for a user whose grants come from them every tile is denied, and
        // the permission notice would give the wrong reason.
        test('says it does not exist in the banner, as well as in the note, once a check confirms it', async () => {
            const page = await mountWith(async (url: string) => {
                const key = keyForUrl(url);
                if (key === 'access-summary') {
                    return jsonResponse({
                        username: 'bob', groups: [], clusterAdmin: false, namespaces: ['*'],
                        rules: { resourceRules: [], nonResourceRules: [], incomplete: false },
                    });
                }
                if (key === 'access-summary-namespaces') {
                    return jsonResponse({ items: [{ namespace: 'ghost', rules: rulesFor() }] });
                }
                if (url.split('?')[0].endsWith('/api/v1/namespaces/ghost')) return jsonResponse({ message: 'nf' }, 404);
                if (key === 'namespaces') return jsonResponse(list(['default']));
                return jsonResponse({ items: [] });
            }, '?namespace=ghost');
            const alerts = Array.from(page.shadowRoot!.querySelectorAll('antrea-alert'))
                .map(a => a.textContent!.replace(/\s+/g, ' ').trim());
            expect(alerts).toEqual(['Namespace ghost does not exist']);
            expect(note(page)).toBe('Namespace ghost does not exist');
        });

        // A tile granted cluster-wide is allowed for a Namespace that does not exist too, so a
        // user with a partial view still has something hidden, and is told.
        test('keeps the notice that something is hidden when only some tiles are denied', async () => {
            const page = await mountWith(async (url: string) => {
                const key = keyForUrl(url);
                if (key === 'access-summary') {
                    return jsonResponse({
                        username: 'bob', groups: [], clusterAdmin: false, namespaces: ['*'],
                        rules: rulesFor(podsList, { apiGroups: [''], resources: ['namespaces'], verbs: ['list'] }),
                    });
                }
                // Events are granted only through a RoleBinding, which a missing Namespace has none of.
                if (key === 'access-summary-namespaces') {
                    return jsonResponse({ items: [{ namespace: 'typo', rules: rulesFor(podsList) }] });
                }
                if (url.split('?')[0].endsWith('/api/v1/namespaces/typo')) return jsonResponse({ message: 'nf' }, 404);
                if (key === 'namespaces') return jsonResponse(list(['default']));
                return jsonResponse({ items: [] });
            }, '?namespace=typo');
            const alerts = Array.from(page.shadowRoot!.querySelectorAll('antrea-alert'))
                .map(a => a.textContent!.replace(/\s+/g, ' ').trim());
            expect(alerts).toEqual([
                'Some information is not shown because your account does not have permission to view it.',
            ]);
            expect(note(page)).toBe('Namespace typo does not exist');
            expect(tileValue(page, 'Pods')).toBe('0');
        });

        test('is not shown when the list is empty, unknown or cut', async () => {
            expect(note(await mount({}, null))).toBeNull();
            const page = await mountWith(boundHandler([], []), '?namespace=team-a');
            expect(note(page)).toBeNull();
        });
    });
});

describe('AntreaOverviewPage — namespace-scoped access', () => {
    function roleBoundSummary(namespace?: string): AccessSummary {
        // A user whose Pod grant exists only through a RoleBinding in team-a: the cluster-scoped
        // summary shows no rules, the team-a one shows the grant.
        return {
            username: 'bob',
            groups: [],
            clusterAdmin: false,
            ...(namespace ? { namespace } : {}),
            rules: {
                resourceRules: namespace === 'team-a' ? [{ apiGroups: [''], resources: ['pods'], verbs: ['list'] }] : [],
                nonResourceRules: [],
                incomplete: false,
            },
            namespaces: ['team-a'],
        };
    }

    async function mountRoleBound() {
        const seenUrls: string[] = [];
        const page = await mountWith(async (url: string) => {
            seenUrls.push(url);
            const key = keyForUrl(url);
            if (key === 'access-summary') return jsonResponse(roleBoundSummary());
            if (key === 'access-summary-namespaces') {
                return jsonResponse({
                    items: new URL(url, 'http://x').searchParams.getAll('namespace')
                        .map(namespace => ({ namespace, rules: roleBoundSummary(namespace).rules })),
                });
            }
            if (key === 'pods') return jsonResponse(list(['p1', 'p2'], 'team-a'));
            return jsonResponse({ items: [] });
        });
        return { page, seenUrls };
    }

    test('seeds the picker from summary.namespaces and starts in the first namespace', async () => {
        const { page, seenUrls } = await mountRoleBound();

        expect(pickerValue(page)).toBe('team-a');
        // No All Namespaces: nothing is granted cluster-wide, so the verdict is denied.
        expect(await optionValues(page)).toEqual(['team-a']);
        expect(seenUrls.some(u => u.includes('access-summary/namespaces?namespace=team-a'))).toBe(true);
        expect(tileValue(page, 'Pods')).toBe('2');
    });

    // The per-namespace answer is cached for a short while and no longer, so a grant added while
    // the page is open shows up when a namespace is selected again, without a reload.
    test('a grant added while the page is open shows when the namespace is selected again', async () => {
        vi.useFakeTimers({ toFake: ['Date'] });
        try {
            const podsList = { apiGroups: [''], resources: ['pods'], verbs: ['list'] };
            const servicesList = { apiGroups: [''], resources: ['services'], verbs: ['list'] };
            let servicesGranted = false;
            const rulesIn = () => ({
                resourceRules: servicesGranted ? [podsList, servicesList] : [podsList],
                nonResourceRules: [], incomplete: false,
            });
            const page = await mountWith(async (url: string) => {
                const key = keyForUrl(url);
                const u = new URL(url, 'http://x');
                if (key === 'access-summary') {
                    return jsonResponse({
                        username: 'bob', groups: [], clusterAdmin: false, namespaces: ['team-a', 'team-b'],
                        rules: { resourceRules: [], nonResourceRules: [], incomplete: false },
                    });
                }
                if (key === 'access-summary-namespaces') {
                    return jsonResponse({
                        items: u.searchParams.getAll('namespace').map(namespace => ({ namespace, rules: rulesIn() })),
                    });
                }
                if (key === 'pods' || key === 'services') return jsonResponse(list(['x'], 'team-a'));
                return jsonResponse({ items: [] });
            });
            expect(hasTile(page, 'Services')).toBe(false);

            servicesGranted = true;
            await choose(page, 'team-b');
            // Within the cache's lifetime the earlier answer for team-a is still what is known.
            await choose(page, 'team-a');
            expect(hasTile(page, 'Services')).toBe(false);

            vi.setSystemTime(Date.now() + 31_000);
            await choose(page, 'team-b');
            await choose(page, 'team-a');
            expect(hasTile(page, 'Services')).toBe(true);
        } finally {
            vi.useRealTimers();
        }
    });

    // "Some information is not shown" reads as if something else were shown. When the selected
    // scope grants nothing, that is not what is the case, and the page says so.
    describe('when the selected scope grants nothing', () => {
        const alerts = (page: AntreaOverviewPage) =>
            Array.from(page.shadowRoot!.querySelectorAll('antrea-alert')).map(a => ({
                status: a.getAttribute('status'), text: a.textContent!.replace(/\s+/g, ' ').trim(),
            }));
        const podsList = { apiGroups: [''], resources: ['pods'], verbs: ['list'] };
        const rules = (namespace: string) => ({
            resourceRules: namespace === 'team-a' ? [podsList] : [],
            nonResourceRules: [],
            incomplete: false,
        });

        test('says the account has nothing it may see here, in place of the partial notice', async () => {
            const page = await mountWith(async (url: string) => {
                const key = keyForUrl(url);
                const u = new URL(url, 'http://x');
                const ns = u.searchParams.get('namespace') ?? '';
                if (key === 'access-summary') {
                    return jsonResponse({
                        username: 'bob', groups: [], clusterAdmin: false, rules: rules(ns),
                        namespaces: ['team-a', 'team-b'], ...(ns ? { namespace: ns } : {}),
                    });
                }
                if (key === 'access-summary-namespaces') {
                    return jsonResponse({
                        items: u.searchParams.getAll('namespace').map(namespace => ({ namespace, rules: rules(namespace) })),
                    });
                }
                if (key === 'pods') return jsonResponse(list(['p1'], 'team-a'));
                return jsonResponse({ items: [] });
            });
            // team-a grants pods and nothing else, which is a partial view.
            expect(alerts(page).map(a => a.status)).toEqual(['info']);

            // team-b grants nothing at all. The scope and the picker stay as they are.
            await choose(page, 'team-b');

            const shown = alerts(page);
            expect(shown).toHaveLength(1);
            expect(shown[0].status).toBe('warning');
            expect(shown[0].text).toContain('does not have permission to view any of this information');
            expect(shown[0].text).toContain('team-b');
            expect(pickerValue(page)).toBe('team-b');
        });

        // The page stays up, dimmed, while the next namespace loads, and the scope has already
        // changed: the previous namespace's notice must not be restated for the new one.
        test('is not shown for the namespace just picked while it loads', async () => {
            let releasePods: () => void = () => {};
            let holdPods = false;
            const page = await mountWith(async (url: string) => {
                const key = keyForUrl(url);
                const u = new URL(url, 'http://x');
                if (key === 'access-summary') {
                    return jsonResponse({
                        username: 'bob', groups: [], clusterAdmin: false, rules: rules(''),
                        namespaces: ['team-a', 'team-b'],
                    });
                }
                if (key === 'access-summary-namespaces') {
                    return jsonResponse({
                        items: u.searchParams.getAll('namespace').map(namespace => ({ namespace, rules: rules(namespace) })),
                    });
                }
                if (key === 'pods') {
                    if (holdPods) await new Promise<void>(resolve => { releasePods = resolve; });
                    return jsonResponse(list(['p1'], 'team-a'));
                }
                return jsonResponse({ items: [] });
            });
            await choose(page, 'team-b');
            expect(alerts(page).map(a => a.status)).toEqual(['warning']);

            holdPods = true;
            await choose(page, 'team-a');
            // team-a's pods request is still in flight: the load has not finished.
            expect(alerts(page).filter(a => a.status === 'warning')).toEqual([]);

            releasePods();
            await new Promise(r => setTimeout(r, 0));
            await new Promise(r => setTimeout(r, 0));
            await page.updateComplete;
            expect(tileValue(page, 'Pods')).toBe('1');
        });

        test('is also what a user sees when every fetch the gate allowed comes back 403', async () => {
            const page = await mountWith(async (url: string) => {
                if (keyForUrl(url) === 'access-summary') return jsonResponse(fullAccessSummary());
                return jsonResponse({ message: 'forbidden' }, 403);
            });
            const shown = alerts(page);
            expect(shown).toHaveLength(1);
            expect(shown[0].status).toBe('warning');
            expect(shown[0].text).toContain('cluster-wide');
        });

        test('is not shown when part of the page is, which keeps the partial notice', async () => {
            const page = await mountWith(async (url: string) => {
                const key = keyForUrl(url);
                if (key === 'access-summary') return jsonResponse(fullAccessSummary());
                if (key === 'pods') return jsonResponse(list(['p1']));
                return jsonResponse({ message: 'forbidden' }, 403);
            });
            const shown = alerts(page);
            expect(shown).toHaveLength(1);
            expect(shown[0].status).toBe('info');
            expect(shown[0].text).toContain('Some information is not shown');
        });

        // Not a permissions problem, and already explained by the danger alert.
        test('is not shown when the fetches fail for another reason', async () => {
            const page = await mountWith(async (url: string) => {
                if (keyForUrl(url) === 'access-summary') return jsonResponse(fullAccessSummary());
                return jsonResponse({ message: 'boom' }, 500);
            });
            const shown = alerts(page);
            expect(shown.some(a => a.text.includes('does not have permission to view any'))).toBe(false);
            expect(shown.some(a => a.status === 'danger')).toBe(true);
        });
    });

    test('gates tiles on the summary for the selected namespace, not the cluster-scoped one', async () => {
        const { page, seenUrls } = await mountRoleBound();

        expect(hasTile(page, 'Services')).toBe(false);
        expect(seenUrls.some(u => u.split('?')[0].endsWith('/api/v1/namespaces/team-a/pods'))).toBe(true);
    });

    // They authorize nothing in a namespace: a RoleBinding to `view` there puts `list` on both in
    // that namespace's summary, and the requests would only come back 403 on every load.
    test('cluster-scoped tiles are neither fetched nor shown while a namespace is selected', async () => {
        const seenUrls: string[] = [];
        const page = await mountWith(async (url: string) => {
            seenUrls.push(url);
            const key = keyForUrl(url);
            if (key === 'access-summary') return jsonResponse(fullAccessSummary());
            if (key === 'namespaces') return jsonResponse(list(['default', 'kube-system']));
            if (key) return jsonResponse({ items: [] });
            throw new Error(`unexpected fetch to ${url}`);
        });
        expect(hasTile(page, 'Namespaces')).toBe(true);
        expect(hasTile(page, 'Antrea Cluster Network Policies')).toBe(true);
        seenUrls.length = 0;

        await choose(page, 'kube-system');

        expect(hasTile(page, 'Namespaces')).toBe(false);
        expect(hasTile(page, 'Antrea Cluster Network Policies')).toBe(false);
        expect(hasTile(page, 'Pods')).toBe(true);
        const keys = seenUrls.map(keyForUrl);
        expect(keys).not.toContain('namespaces');
        expect(keys).not.toContain('antreaClusterNetworkPolicies');
        // Not hidden from the user either, so the partial notice stays away.
        expect(alertText(page, 'info')).toBeNull();
        // And the picker keeps the list it had.
        expect(await optionValues(page)).toEqual([ALL, 'default', 'kube-system']);
    });

    test('a namespace selected by a user with no cluster-wide grant gets no 403 from the cluster-scoped kinds', async () => {
        const { page, seenUrls } = await mountRoleBound();
        const keys = seenUrls.map(keyForUrl);
        expect(keys).not.toContain('namespaces');
        expect(keys).not.toContain('antreaClusterNetworkPolicies');
        expect(hasTile(page, 'Namespaces')).toBe(false);
        expect(hasTile(page, 'Antrea Cluster Network Policies')).toBe(false);
    });
});

describe('AntreaOverviewPage — Recent Events', () => {
    test('sorts a repeating events.k8s.io event by series.lastObservedTime', async () => {
        const repeating = {
            ...event({ name: 'repeating', reason: 'FailedScheduling', lastTimestamp: '' }),
            eventTime: '2026-01-01T00:00:00Z',
            series: { lastObservedTime: '2026-01-03T00:00:00Z' },
        };
        const page = await mount({
            events: { items: [event({ name: 'other', lastTimestamp: '2026-01-02T00:00:00Z', reason: 'Started' }), repeating] },
        });

        expect(listRows(page, 'Recent Events').map(r => r[3])).toEqual(['FailedScheduling', 'Started']);
    });

    test('notes when the events list was truncated and the newest may be missing', async () => {
        const page = await mount({
            events: { items: [event({ name: 'a' })], metadata: { continue: 'abc' } },
        });
        expect(page.shadowRoot!.querySelector('antrea-card[heading="Recent Events"] antrea-alert')).not.toBeNull();

        const untruncated = await mount({ events: list1(event({ name: 'a' })) });
        expect(untruncated.shadowRoot!.querySelector('antrea-card[heading="Recent Events"] antrea-alert')).toBeNull();
    });

    test('renders events newest first, capped at the display limit', async () => {
        // More than the 20 shown, oldest first, so that taking the first 20 of the page instead
        // of the newest 20 would show different rows.
        const items = Array.from({ length: 25 }, (_, i) => event({
            name: `ev-${i}`,
            reason: `Reason${i}`,
            lastTimestamp: `2026-01-${String(i + 1).padStart(2, '0')}T00:00:00Z`,
        }));
        const page = await mount({ events: { items } });

        const reasons = listRows(page, 'Recent Events').map(r => r[3]);
        expect(reasons).toHaveLength(20);
        expect(reasons[0]).toBe('Reason24');
        expect(reasons[19]).toBe('Reason5');
    });

    test('renders events newest first', async () => {
        const page = await mount({
            events: {
                items: [
                    event({ name: 'old', lastTimestamp: '2026-01-01T00:00:00Z', reason: 'Started' }),
                    event({ name: 'new', lastTimestamp: '2026-01-02T00:00:00Z', reason: 'Killing' }),
                ],
            },
        });

        expect(listRows(page, 'Recent Events').map(r => r[3])).toEqual(['Killing', 'Started']);
    });

    // Events expire after an hour by default, so a quiet namespace has none: that is an answer,
    // and a missing card would read as a missing panel.
    test('the Recent Events card says so when there are no events', async () => {
        const page = await mount({});
        expect(hasTile(page, 'Recent Events')).toBe(true);
        expect(page.shadowRoot!.querySelector('antrea-card[heading="Recent Events"]')!.textContent)
            .toContain('No recent events');
        expect(listRows(page, 'Recent Events')).toEqual([]);
    });

    test('no Recent Events card when the user may not list events', async () => {
        const summary: AccessSummary = {
            ...fullAccessSummary(),
            clusterAdmin: false,
            rules: {
                resourceRules: [{ apiGroups: [''], resources: ['pods'], verbs: ['list'] }],
                nonResourceRules: [],
                incomplete: false,
            },
        };
        const page = await mount({ pods: list(['p1'], 'default') }, summary);
        expect(hasTile(page, 'Recent Events')).toBe(false);
    });
});
