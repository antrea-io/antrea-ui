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

    test('Antrea ClusterNetworkPolicies and NetworkPolicies are separate tiles', async () => {
        const page = await mount({
            antreaClusterNetworkPolicies: list(['acnp1']),
            antreaNetworkPolicies: list(['anp1', 'anp2'], 'default'),
        });

        expect(tileValue(page, 'Antrea Network Policies')).toBe('2');
        expect(tileValue(page, 'Antrea ClusterNetworkPolicies')).toBe('1');
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

    // A ServiceAccount that can read workloads but not NetworkPolicies is a realistic split
    // (workload-reader roles rarely also grant policy visibility), and exercises the mix of
    // core/v1, apps/v1 and two different NetworkPolicy gates together, rather than the single
    // resource the test above denies.
    test('a SA that can list workloads but not NetworkPolicies sees workload tiles only', async () => {
        const summary: AccessSummary = {
            ...fullAccessSummary(),
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
            if (key === 'access-summary') {
                const ns = new URL(url, 'http://x').searchParams.get('namespace') ?? undefined;
                return jsonResponse(roleBoundSummary(ns));
            }
            if (key === 'pods') return jsonResponse(list(['p1', 'p2'], 'team-a'));
            return jsonResponse({ items: [] });
        });
        return { page, seenUrls };
    }

    test('seeds the picker from summary.namespaces and starts in the first accessible namespace', async () => {
        const { page, seenUrls } = await mountRoleBound();

        const select = page.shadowRoot!.querySelector('#ns-select') as HTMLSelectElement;
        expect(select.value).toBe('team-a');
        // No All Namespaces: nothing is granted cluster-wide, so it could only be an empty page.
        expect(Array.from(select.options).map(o => o.value)).toEqual(['team-a']);
        expect(seenUrls.some(u => u.includes('access-summary?namespace=team-a'))).toBe(true);
        expect(tileValue(page, 'Pods')).toBe('2');
    });

    // summary.namespaces is the RoleBinding-subject heuristic, a superset of what the user can see:
    // its first entry may grant nothing this page shows. The per-namespace summaries say which do.
    describe('choosing the namespace to start in', () => {
        const podsList = { apiGroups: [''], resources: ['pods'], verbs: ['list'] };
        function rulesFor(...rules: { apiGroups: string[], resources: string[], verbs: string[] }[]) {
            return { resourceRules: rules, nonResourceRules: [], incomplete: false };
        }

        async function mountNamespaces(
            namespaces: string[],
            perNamespace: Record<string, unknown>,
            batch: 'ok' | 'fails' = 'ok',
        ) {
            const asked: string[][] = [];
            const page = await mountWith(async (url: string) => {
                const key = keyForUrl(url);
                const u = new URL(url, 'http://x');
                if (key === 'access-summary') {
                    return jsonResponse({
                        username: 'bob', groups: [], clusterAdmin: false, rules: rulesFor(), namespaces,
                        ...(u.searchParams.get('namespace') ? { namespace: u.searchParams.get('namespace') } : {}),
                    });
                }
                if (key === 'access-summary-namespaces') {
                    asked.push(u.searchParams.getAll('namespace'));
                    if (batch === 'fails') return jsonResponse({ message: 'boom' }, 503);
                    return jsonResponse({
                        items: u.searchParams.getAll('namespace').map(namespace => ({
                            namespace, rules: perNamespace[namespace] ?? rulesFor(),
                        })),
                    });
                }
                return jsonResponse({ items: [] });
            });
            return { page, asked };
        }

        const selected = (page: AntreaOverviewPage) =>
            (page.shadowRoot!.querySelector('#ns-select') as HTMLSelectElement).value;

        test('starts in the first namespace that grants something to show, not the first listed', async () => {
            const { page, asked } = await mountNamespaces(['team-a', 'team-b'], { 'team-b': rulesFor(podsList) });
            expect(asked).toEqual([['team-a', 'team-b']]);
            expect(selected(page)).toBe('team-b');
        });

        test('prefers a namespace that is merely unknown to one known to grant nothing', async () => {
            const { page } = await mountNamespaces(['team-a', 'team-b'], {
                'team-b': { resourceRules: [], nonResourceRules: [], incomplete: true },
            });
            expect(selected(page)).toBe('team-b');
        });

        test('falls back to the first namespace when none is known to grant anything', async () => {
            const { page } = await mountNamespaces(['team-a', 'team-b'], {});
            expect(selected(page)).toBe('team-a');
        });

        test('fails open to the first namespace when the answer cannot be fetched', async () => {
            const { page } = await mountNamespaces(['team-a', 'team-b'], { 'team-b': rulesFor(podsList) }, 'fails');
            expect(selected(page)).toBe('team-a');
        });

        test('asks about no more namespaces than a request allows', async () => {
            const many = Array.from({ length: 12 }, (_, i) => `ns-${String(i).padStart(2, '0')}`);
            const { asked } = await mountNamespaces(many, {});
            expect(asked).toHaveLength(1);
            expect(asked[0]).toEqual(many.slice(0, 10));
        });
    });

    // All Namespaces is gated on the cluster-scoped summary, so for a user whose grants all come
    // from RoleBindings it can only show the access notice.
    describe('the All Namespaces option', () => {
        const optionValues = (page: AntreaOverviewPage) =>
            Array.from((page.shadowRoot!.querySelector('#ns-select') as HTMLSelectElement).options).map(o => o.value);

        test('is offered to a user with a cluster-wide grant for something the page shows', async () => {
            const page = await mount({ namespaces: list(['default', 'kube-system']) });
            expect(optionValues(page)).toEqual(['', 'default', 'kube-system']);
        });

        // Unlike the choice of where to start, this keeps namespaces in the check. This user starts
        // on All Namespaces, so hiding it would leave the <select> showing a namespace while the
        // page shows the cluster view, and picking that namespace would fire no change event.
        test('is offered to a user whose only cluster-wide grant is listing namespaces', async () => {
            const onlyNamespaces: AccessSummary = {
                username: 'bob', groups: [], clusterAdmin: false, namespaces: ['*'],
                rules: {
                    resourceRules: [{ apiGroups: [''], resources: ['namespaces'], verbs: ['list'] }],
                    nonResourceRules: [], incomplete: false,
                },
            };
            const page = await mount({ namespaces: list(['default']) }, onlyNamespaces);
            expect(optionValues(page)).toEqual(['', 'default']);
            expect((page.shadowRoot!.querySelector('#ns-select') as HTMLSelectElement).value).toBe('');
        });

        test('is not offered to a user whose grants all come from RoleBindings', async () => {
            const { page } = await mountRoleBound();
            expect(optionValues(page)).not.toContain('');
        });
    });

    // "Some information is not shown" reads as if something else were shown. When the selected
    // scope grants nothing, that is not what is the case, and the page says so.
    describe('when the selected scope grants nothing', () => {
        const alerts = (page: AntreaOverviewPage) =>
            Array.from(page.shadowRoot!.querySelectorAll('antrea-alert')).map(a => ({
                status: a.getAttribute('status'), text: a.textContent!.replace(/\s+/g, ' ').trim(),
            }));

        test('says the account has nothing it may see here, in place of the partial notice', async () => {
            const podsList = { apiGroups: [''], resources: ['pods'], verbs: ['list'] };
            const page = await mountWith(async (url: string) => {
                const key = keyForUrl(url);
                const u = new URL(url, 'http://x');
                const ns = u.searchParams.get('namespace') ?? '';
                const rules = (namespace: string) => ({
                    resourceRules: namespace === 'team-a' ? [podsList] : [],
                    nonResourceRules: [],
                    incomplete: false,
                });
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

            // team-b grants nothing at all.
            const select = page.shadowRoot!.querySelector('#ns-select') as HTMLSelectElement;
            select.value = 'team-b';
            select.dispatchEvent(new Event('change'));
            await page.updateComplete;
            await new Promise(r => setTimeout(r, 0));
            await new Promise(r => setTimeout(r, 0));
            await page.updateComplete;

            const shown = alerts(page);
            expect(shown).toHaveLength(1);
            expect(shown[0].status).toBe('warning');
            expect(shown[0].text).toContain('does not have permission to view any of this information');
            expect(shown[0].text).toContain('team-b');
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

    test('a cluster-scoped tile says so while a namespace is selected', async () => {
        const page = await mount({
            namespaces: list(['default', 'kube-system']),
            antreaClusterNetworkPolicies: list(['acnp1']),
        });
        const scopeNote = () => page.shadowRoot!.querySelector('antrea-card[heading="Antrea ClusterNetworkPolicies"] .scope-note');
        expect(scopeNote()).toBeNull();

        const select = page.shadowRoot!.querySelector('#ns-select') as HTMLSelectElement;
        select.value = 'kube-system';
        select.dispatchEvent(new Event('change'));
        await page.updateComplete;
        await new Promise(r => setTimeout(r, 0));
        await new Promise(r => setTimeout(r, 0));
        await page.updateComplete;

        expect(scopeNote()?.textContent).toContain('Cluster-wide');
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

    test('no Recent Events card when there are no events', async () => {
        const page = await mount({});
        expect(hasTile(page, 'Recent Events')).toBe(false);
    });
});
