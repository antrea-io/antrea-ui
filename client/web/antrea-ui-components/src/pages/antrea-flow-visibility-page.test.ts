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

import { FlowStore } from '../lib/flow-store';
import type { SeenNames } from '../lib/seen-names';
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';
import './antrea-flow-visibility-page';
import { SCOPE_CHANGE_EVENT, SCOPE_CHOSEN_EVENT } from '../lib/namespace-scope';
import type { ScopeChangeDetail, ScopeChosenDetail } from '../lib/namespace-scope';
import type { AntreaFlowVisibilityPage } from './antrea-flow-visibility-page';
import { searchFilterOptions, typedNamespace, typedPod, typedService } from './antrea-flow-visibility-page';
import { FLOW_QUERY_STATE_EVENT } from '../lib/flow-stream';
import type { FlowQuery, FlowQueryStateDetail, FlowStreamFilter } from '../lib/flow-stream';
import { resetAccessSummary } from '../lib/access-api';
import {
    Flow,
    FlowType,
    FlowEndReason,
    IPVersion,
    NetworkPolicyType,
    NetworkPolicyRuleAction,
    EndpointDisclosure,
} from '../lib/flow-types';

interface FlowOverrides {
    /** Empty on purpose in one test: the Flow Aggregator is not obliged to populate it. */
    id?: string;
    srcPod?: string;
    dstPod?: string;
    ingressPolicy?: string;
    ingressAction?: NetworkPolicyRuleAction;
    ingressPolicyType?: NetworkPolicyType;
    egressPolicy?: string;
    egressAction?: NetworkPolicyRuleAction;
    /** Part of connectionKey, so two records differing only here are two connections. */
    dstPort?: number;
    srcIP?: string;
    dstIP?: string;
    srcNs?: string;
    dstNs?: string;
    srcDisclosure?: EndpointDisclosure;
    dstDisclosure?: EndpointDisclosure;
    svcDisclosure?: EndpointDisclosure;
    destService?: string;
    flowType?: FlowType;
}

function makeFlow(overrides: FlowOverrides = {}): Flow {
    return {
        id: overrides.id ?? `flow-${Math.random()}`,
        startTs: '2026-03-25T00:00:00Z',
        endTs: '2026-03-25T00:01:00Z',
        endReason: FlowEndReason.Unspecified,
        // connectionKey() (flow-types.ts) is keyed on source/destination IP, not pod name —
        // entries with distinct pod names but the same IPs collapse into a single FlowStore
        // entry. Tests distinguishing entries by pod name must also vary the source IP.
        ip: { version: IPVersion.IPv4, source: overrides.srcIP ?? '10.0.0.1', destination: overrides.dstIP ?? '10.0.0.2' },
        transport: { protocolNumber: 6, sourcePort: 12345, destinationPort: overrides.dstPort ?? 80 },
        k8s: {
            flowType: overrides.flowType ?? FlowType.InterNode,
            sourceDisclosure: overrides.srcDisclosure ?? EndpointDisclosure.Full,
            destinationDisclosure: overrides.dstDisclosure ?? EndpointDisclosure.Full,
            destinationServiceDisclosure: overrides.svcDisclosure ?? EndpointDisclosure.Full,
            sourcePodNamespace: overrides.srcNs ?? 'default',
            sourcePodName: overrides.srcPod ?? 'client-abc12',
            sourcePodUid: '',
            sourceNodeName: 'node-1',
            sourceNodeUid: '',
            destinationPodNamespace: overrides.dstNs ?? 'default',
            destinationPodName: overrides.dstPod ?? 'server-xyz34',
            destinationPodUid: '',
            destinationNodeName: 'node-2',
            destinationNodeUid: '',
            destinationClusterIp: '',
            destinationServicePort: 0,
            destinationServicePortName: overrides.destService ?? '',
            destinationServiceUid: '',
            ingressNetworkPolicyType: overrides.ingressPolicyType ?? (overrides.ingressPolicy ? NetworkPolicyType.K8s : NetworkPolicyType.Unspecified),
            ingressNetworkPolicyNamespace: '',
            ingressNetworkPolicyName: overrides.ingressPolicy ?? '',
            ingressNetworkPolicyUid: '',
            ingressNetworkPolicyRuleName: '',
            ingressNetworkPolicyRuleAction: overrides.ingressAction ?? (overrides.ingressPolicy ? NetworkPolicyRuleAction.Allow : NetworkPolicyRuleAction.NoAction),
            egressNetworkPolicyType: overrides.egressPolicy ? NetworkPolicyType.K8s : NetworkPolicyType.Unspecified,
            egressNetworkPolicyNamespace: '',
            egressNetworkPolicyName: overrides.egressPolicy ?? '',
            egressNetworkPolicyUid: '',
            egressNetworkPolicyRuleName: '',
            egressNetworkPolicyRuleAction: overrides.egressAction ?? (overrides.egressPolicy ? NetworkPolicyRuleAction.Allow : NetworkPolicyRuleAction.NoAction),
            egressName: '',
            egressIp: '',
            egressNodeName: '',
            egressNodeUid: '',
            egressUid: '',
        },
        stats: { packetTotalCount: 1, packetDeltaCount: 1, octetTotalCount: 100, octetDeltaCount: 100 },
        reverseStats: { packetTotalCount: 1, packetDeltaCount: 1, octetTotalCount: 100, octetDeltaCount: 100 },
    };
}

function sseResponse(chunks: string[], status = 200): Response {
    const encoder = new TextEncoder();
    const stream = new ReadableStream<Uint8Array>({
        start(controller) {
            for (const chunk of chunks) controller.enqueue(encoder.encode(chunk));
        },
    });
    return new Response(stream, { status });
}

function flowEventChunk(flows: Flow[]): string {
    return `event: flow\ndata: ${JSON.stringify({ flows })}\n\n`;
}

/** Ticks an option of the open filter menu by its label. */
function toggleOption(page: AntreaFlowVisibilityPage, label: string): void {
    const option = Array.from(page.shadowRoot!.querySelectorAll('.multiselect-option'))
        .find(el => el.textContent!.trim() === label);
    if (!option) throw new Error(`no filter option "${label}" in [${menuOptions(page).join(', ')}]`);
    option.querySelector<HTMLInputElement>('input[type="checkbox"]')!
        .dispatchEvent(new Event('change', { bubbles: true }));
}

function menuOptions(page: AntreaFlowVisibilityPage): string[] {
    return Array.from(page.shadowRoot!.querySelectorAll('.multiselect-option')).map(el => el.textContent!.trim());
}

function streamCalls(fetchMock: { mock: { calls: unknown[][] } }): unknown[][] {
    return fetchMock.mock.calls.filter(c => typeof c[0] === 'string' && c[0].includes('/api/v1/flows/stream'));
}

let el: AntreaFlowVisibilityPage | undefined;

beforeEach(() => {
    vi.useFakeTimers();
});

afterEach(async () => {
    if (el) {
        el.remove();
        el = undefined;
    }
    vi.useRealTimers();
    vi.unstubAllGlobals();
    // The access summaries are memoized across calls, and one test's answer must not be the next
    // one's.
    resetAccessSummary();
    // The page writes the scope into the URL, so a test that selected one would otherwise leak
    // it into the next through the shared jsdom location.
    window.history.replaceState(null, '', '/');
});

function jsonResponse(body: unknown, status = 200): Response {
    return new Response(JSON.stringify(body), { status });
}

/** What the selector's options are meant to be, in the terms the tests think in. The default
 * offers the namespace the fixtures' flows are in, so a test that does not care about the
 * selector still gets a stream. `mount` turns it into the access summary the page derives them
 * from. */
interface FlowAccessFixture {
    /** The namespaces the summary names, from the caller's RoleBindings. */
    namespaces: string[]
    /** `unknown`: no rule grants flows cluster-wide but the API server could not enumerate its
     * rules, as with a webhook authorizer. */
    clusterWide: 'allowed' | 'unknown' | 'denied'
    /** The caller may list namespaces, so the summary names none ("*") and the page lists them
     * itself: `names` are what that returns, or it fails. */
    lister?: { names: string[], cut?: boolean } | 'fails'
    /** Namespaces that exist besides the ones the lister names, for a fresh check of one the
     * list did not hold. */
    alsoExist?: string[]
    /** Incomplete rules, as with a webhook authorizer, without anything else changing. */
    incomplete?: boolean
    /** The verb of the cluster-wide flows grant when `clusterWide` is `allowed`. */
    verb?: 'watch' | 'list'
}

function flowAccessFixture(
    opts: Partial<FlowAccessFixture> = {},
): FlowAccessFixture {
    return {
        namespaces: opts.namespaces ?? ['default'],
        clusterWide: opts.clusterWide ?? 'denied',
        lister: opts.lister,
        alsoExist: opts.alsoExist,
        incomplete: opts.incomplete,
        verb: opts.verb,
    };
}

const flowsRule = (verb: 'watch' | 'list') => ({ verbs: [verb], apiGroups: ['observability.antrea.io'], resources: ['flows'] });

/** GET /api/v1/access-summary: the cluster-scoped one, which holds the grant for a cluster-wide
 * caller and names the namespaces the page offers. */
function accessSummaryResponse(f: FlowAccessFixture): Response {
    return jsonResponse({
        username: 'alice', groups: [], clusterAdmin: false,
        namespaces: f.lister ? ['*'] : f.namespaces,
        rules: {
            resourceRules: f.clusterWide === 'allowed' ? [flowsRule(f.verb ?? 'watch')] : [],
            nonResourceRules: [],
            incomplete: f.clusterWide === 'unknown' || !!f.incomplete,
        },
    });
}

// Does NOT advance timers: the stream only opens once the settings fetch in connectedCallback()
// resolves, so a test that wants to observe or count that first stream request must attach its
// listeners before letting the microtask queue drain.
// `search` is the page URL, whose `namespace` parameter is where the scope lives: the
// default selects one, since a page with no scope selected deliberately opens no stream at all
// and almost every test here is about a stream that is open. The access summaries the selector's
// options come from are answered ahead of `fetchImpl`, so the individual tests only have to
// describe the stream.
async function mount(
    fetchImpl: (url: string, init?: RequestInit) => Promise<Response>,
    opts: { search?: string; flowAccess?: () => FlowAccessFixture; flowQuery?: FlowQuery } = {},
): Promise<AntreaFlowVisibilityPage> {
    // A page that mounts twice in one test must not be answered from the first one's summaries.
    resetAccessSummary();
    window.history.replaceState(null, '', opts.search ?? '/?namespace=default');
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
        const single = typeof url === 'string' ? /\/api\/v1\/k8s\/api\/v1\/namespaces\/([^/?]+)$/.exec(url) : null;
        if (single) {
            const f = (opts.flowAccess ?? flowAccessFixture)();
            const names = [...(f.lister && f.lister !== 'fails' ? f.lister.names : []), ...(f.alsoExist ?? [])];
            return names.includes(single[1]) ? jsonResponse({ metadata: { name: single[1] } }) : jsonResponse({ message: 'not found' }, 404);
        }
        if (typeof url === 'string' && url.includes('/api/v1/k8s/api/v1/namespaces')) {
            const lister = (opts.flowAccess ?? flowAccessFixture)().lister;
            return !lister || lister === 'fails'
                ? jsonResponse({ message: 'boom' }, 500)
                : jsonResponse({
                    items: lister.names.map(name => ({ metadata: { name } })),
                    ...(lister.cut ? { metadata: { continue: 'more' } } : {}),
                });
        }
        if (typeof url === 'string' && url.includes('/api/v1/access-summary')) {
            return accessSummaryResponse((opts.flowAccess ?? flowAccessFixture)());
        }
        return fetchImpl(url, init);
    }));
    el = document.createElement('antrea-flow-visibility-page') as AntreaFlowVisibilityPage;
    el.flowQuery = opts.flowQuery;
    document.body.appendChild(el);
    await el.updateComplete;
    // The namespace list has to resolve before any stream can open (see onSessionReady), so let
    // that fetch and the render it triggers land before returning.
    await vi.advanceTimersByTimeAsync(0);
    await el.updateComplete;
    return el;
}

// The observed-namespace selector is a combobox: an input that shows the scope in force while
// closed, and a listbox of options while open. `data-value` is the scope itself; the text is only
// its label.
function scopeInput(page: AntreaFlowVisibilityPage): HTMLInputElement {
    return page.shadowRoot!.querySelector<HTMLInputElement>('#observed-ns')!;
}

function scopeValue(page: AntreaFlowVisibilityPage): string | undefined {
    return scopeInput(page).dataset.value;
}

async function openScope(page: AntreaFlowVisibilityPage): Promise<void> {
    scopeInput(page).dispatchEvent(new FocusEvent('focus'));
    await page.updateComplete;
}

/** What the open menu offers, as scope values paired with their labels. */
async function scopeMenu(page: AntreaFlowVisibilityPage): Promise<{ value: string; text: string }[]> {
    await openScope(page);
    return Array.from(page.shadowRoot!.querySelectorAll<HTMLElement>('#observed-ns-list [role="option"]'))
        .map(o => ({ value: o.dataset.value!, text: o.textContent!.trim() }));
}

async function chooseScope(page: AntreaFlowVisibilityPage, value: string): Promise<void> {
    await openScope(page);
    const option = Array.from(page.shadowRoot!.querySelectorAll<HTMLElement>('#observed-ns-list [role="option"]'))
        .find(o => o.dataset.value === value)!;
    option.click();
    await page.updateComplete;
}

/** Types into the open combobox, past the pause the search waits for. */
async function typeScope(page: AntreaFlowVisibilityPage, value: string): Promise<void> {
    const input = scopeInput(page);
    input.value = value;
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await vi.advanceTimersByTimeAsync(300);
    await page.updateComplete;
}

async function pressInScope(page: AntreaFlowVisibilityPage, key: string): Promise<void> {
    scopeInput(page).dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }));
    await page.updateComplete;
}

// The observed-namespace selector, which replaced the hardcoded cluster scope this page used
// while there was no UI to pick one. The Flow Aggregator requires every stream to name exactly
// one scope and rejects a request naming none with a 400, so the selector is not a filter: with
// nothing selected the page opens no stream at all. It does not land there, though - the page
// defaults to a scope, and reaches the no-selection state only when there is nothing to select.
describe('AntreaFlowVisibilityPage — observed-namespace selector', () => {
    // Also the URL beating the default: the list here offers only `default`, so `ns-a` can have
    // come from nowhere else.
    test('the stream names the namespace the URL carries', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        await mount(fetchMock, { search: '/?namespace=ns-a' });

        const calls = streamCalls(fetchMock);
        expect(calls).toHaveLength(1);
        const params = new URL(calls[0][0] as string, 'http://example.test').searchParams;
        expect(params.get('observedNamespace')).toBe('ns-a');
        expect(params.get('clusterWide')).toBeNull();
    });

    // Landing with nothing selected made the common case - the user who may observe exactly one
    // namespace - open a menu to pick the only thing in it before the page showed anything.
    test('the first namespace is selected by default when the URL names none', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        const page = await mount(fetchMock, {
            search: '/',
            flowAccess: () => flowAccessFixture({
                namespaces: ['ns-b', 'ns-a'],
            }),
        });

        const calls = streamCalls(fetchMock);
        expect(calls).toHaveLength(1);
        // Sorted, not response order: which namespace a reload lands on should not depend on
        // however the backend happened to enumerate them.
        expect(new URL(calls[0][0] as string, 'http://example.test').searchParams.get('observedNamespace'))
            .toBe('ns-a');
        expect(scopeValue(page)).toBe('ns-a');
        // Not written to the URL: a default is not a choice. Written, a reload would take it as
        // an explicit one, and a later change to what the default would be would be ignored.
        expect(window.location.search).toBe('');
    });

    // An operator who can see the whole cluster should not be defaulted into one namespace: it is
    // what this page showed before the selector existed, and the only unredacted scope.
    test('cluster scope is the default where the user holds it', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        const page = await mount(fetchMock, {
            search: '/',
            flowAccess: () => flowAccessFixture({ clusterWide: 'allowed' }),
        });

        const params = new URL(streamCalls(fetchMock)[0][0] as string, 'http://example.test').searchParams;
        expect(params.get('clusterWide')).toBe('true');
        expect(params.get('observedNamespace')).toBeNull();
        expect(scopeValue(page)).toBe('__cluster_wide__');
        expect(window.location.search).toBe('');
    });

    // A shared link, or the user's last pick, names a scope the user may not hold. It is used as
    // given: replacing it would hide what the link asked for. The stream opens, the Flow
    // Aggregator answers, and picking another scope replaces it.
    test('a cluster scope in the URL is used as given, even where the user is known not to hold it', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        const page = await mount(fetchMock, {
            search: '/?namespace=_all',
            flowAccess: () => flowAccessFixture({ clusterWide: 'denied' }),
        });

        const params = new URL(streamCalls(fetchMock)[0][0] as string, 'http://example.test').searchParams;
        expect(params.get('clusterWide')).toBe('true');
        expect(params.get('observedNamespace')).toBeNull();
        // Selected in the picker, which offers it for a scope in force even when it would not
        // otherwise.
        expect(scopeValue(page)).toBe('__cluster_wide__');
        expect((await scopeMenu(page)).map(o => o.value)).toContain('__cluster_wide__');
        expect(window.location.search).toBe('?namespace=_all');
    });

    // Access is not checked here: whether the user may observe the namespace is the Flow
    // Aggregator's to say, when the stream opens.
    test('a namespace in the URL is restored and offered like any other', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        const page = await mount(fetchMock, {
            search: '/?namespace=flow-c',
            flowAccess: () => flowAccessFixture({
                namespaces: ['flow-a', 'flow-c'],
            }),
        });

        const params = new URL(streamCalls(fetchMock)[0][0] as string, 'http://example.test').searchParams;
        expect(params.get('observedNamespace')).toBe('flow-c');
        expect((await scopeMenu(page)).some(o => o.value === 'flow-c')).toBe(true);
    });

    // The escape hatch, which the rule above must not close: an unlisted namespace is honoured,
    // because the list can under-report and the Flow Aggregator authorizes either way.
    test('a namespace the response never mentioned is still restored from the URL', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        await mount(fetchMock, {
            search: '/?namespace=ns-unlisted',
            flowAccess: () => flowAccessFixture({ lister: 'fails' }),
        });

        expect(new URL(streamCalls(fetchMock)[0][0] as string, 'http://example.test').searchParams
            .get('observedNamespace')).toBe('ns-unlisted');
    });

    test('no stream opens when there is no namespace to observe', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        const page = await mount(fetchMock, {
            search: '/',
            flowAccess: () => flowAccessFixture({ namespaces: [], clusterWide: 'denied' }),
        });

        expect(streamCalls(fetchMock)).toHaveLength(0);
        // And it stays that way: nothing retries into a scope-less request later either.
        await vi.advanceTimersByTimeAsync(60_000);
        expect(streamCalls(fetchMock)).toHaveLength(0);
        expect(page.shadowRoot!.querySelector('tbody')).toBeNull();
    });

    test('the map view opens no stream either, and offers the same selector', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        const page = await mount(fetchMock, {
            search: '/',
            flowAccess: () => flowAccessFixture({ namespaces: [], clusterWide: 'denied' }),
        });
        page.viewMode = 'map';
        await page.updateComplete;

        expect(streamCalls(fetchMock)).toHaveLength(0);
        expect(page.shadowRoot!.querySelector('#graph-svg')).toBeNull();
        expect(scopeInput(page)).not.toBeNull();
    });

    test('changing the scope reconnects with the new namespace, and records it in the URL', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        const page = await mount(fetchMock, {
            search: '/?namespace=default',
            flowAccess: () => flowAccessFixture({
                namespaces: ['default', 'ns-b'],
            }),
        });
        expect(streamCalls(fetchMock)).toHaveLength(1);

        await chooseScope(page, 'ns-b');
        await vi.advanceTimersByTimeAsync(0);

        const calls = streamCalls(fetchMock);
        expect(calls).toHaveLength(2);
        expect(new URL(calls[1][0] as string, 'http://example.test').searchParams.get('observedNamespace'))
            .toBe('ns-b');
        expect(window.location.search).toContain('namespace=ns-b');
    });

    test('cluster scope is offered unless the user is known not to hold it', async () => {
        const page = await mount(async () => sseResponse([]), {
            search: '/',
            flowAccess: () => flowAccessFixture({ clusterWide: 'denied' }),
        });
        expect((await scopeMenu(page)).map(o => o.value)).not.toContain('__cluster_wide__');

        const withCluster = await mount(async () => sseResponse([]), {
            search: '/',
            flowAccess: () => flowAccessFixture({ clusterWide: 'allowed' }),
        });
        const clusterOption = (await scopeMenu(withCluster)).find(o => o.value === '__cluster_wide__');
        expect(clusterOption).toBeDefined();
        expect(clusterOption!.text).toContain('All Namespaces');
    });

    // Mounted on a namespace rather than letting the default pick cluster scope, so that the
    // change handler is what is under test here and not the default.
    test('selecting cluster scope asks for clusterWide, not a namespace', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        const page = await mount(fetchMock, {
            search: '/?namespace=default',
            flowAccess: () => flowAccessFixture({ clusterWide: 'allowed' }),
        });

        await chooseScope(page, '__cluster_wide__');
        await page.updateComplete;
        await vi.advanceTimersByTimeAsync(0);

        const calls = streamCalls(fetchMock);
        expect(calls).toHaveLength(2);
        const params = new URL(calls[1][0] as string, 'http://example.test').searchParams;
        expect(params.get('clusterWide')).toBe('true');
        expect(params.get('observedNamespace')).toBeNull();
        expect(window.location.search).toContain('namespace=_all');
    });

    // Nothing is checked, so the default is only the first namespace the user's bindings name.
    test('the default is the first namespace offered', async () => {
        const page = await mount(async () => sseResponse([]), {
            search: '/',
            flowAccess: () => flowAccessFixture({
                namespaces: ['ns-a', 'ns-b'],
            }),
        });
        expect(scopeValue(page)).toBe('ns-a');
        const offered = (await scopeMenu(page)).find(o => o.value === 'ns-b');
        expect(offered).toBeDefined();
        expect(offered!.text).not.toContain('not authorized');
    });

    // A cluster-wide caller's grant comes from the cluster-scoped summary, so failing to list the
    // namespaces must not take their scope away - or leave anyone without a way to choose one.
    test('a failed namespace list still offers cluster scope, and a way to name a namespace', async () => {
        const page = await mount(async () => sseResponse([]), {
            search: '/',
            flowAccess: () => flowAccessFixture({
                namespaces: ['ns-a'], clusterWide: 'allowed', lister: 'fails',
            }),
        });
        expect(scopeValue(page)).toBe('__cluster_wide__');
        expect(page.shadowRoot!.textContent).toContain('Namespace not listed');
    });

    // A user who may observe no namespace is a real answer the backend is entitled to give, not a
    // failure: it has to read as an explanation rather than an error.
    test('an empty namespace list is an explanation, not an error', async () => {
        const page = await mount(async () => sseResponse([]), {
            search: '/',
            flowAccess: () => flowAccessFixture({ namespaces: [], clusterWide: 'denied' }),
        });

        expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')).toBeNull();
        expect(page.shadowRoot!.textContent).toContain('not authorized to observe flows in any namespace');
        expect(page.shadowRoot!.querySelector('p.text-muted a')?.getAttribute('href')).toBe('https://github.com/antrea-io/antrea-ui/blob/main/docs/authentication.md#flow-data-is-per-user');
        // An empty list is one a webhook authorizer may still grant a namespace to, so the user
        // can name one.
        expect(page.shadowRoot!.textContent).toContain('Namespace not listed');
    });

    // An empty list that may be short - cut, or one that could not be fetched - cannot show
    // the user holds nothing, so it must not say so. It says what to do about it instead: the
    // free-text box is a real escape hatch, honoured whether or not the namespace is listed.
    test('an empty list that may be short does not claim the user is not authorized', async () => {
        const page = await mount(async () => sseResponse([]), {
            search: '/',
            flowAccess: () => flowAccessFixture({ namespaces: [], clusterWide: 'denied', lister: 'fails' }),
        });

        expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')).toBeNull();
        expect(page.shadowRoot!.textContent).not.toContain('not authorized to observe flows in any namespace');
        expect(page.shadowRoot!.textContent).toContain('no complete list of namespaces');
        expect(page.shadowRoot!.textContent).toContain('Namespace not listed');
    });

    // The escape hatch for an empty, unknown or cut list, and the reason the note no longer tells anyone to
    // hand-edit a URL.
    test('a namespace named in the free-text box opens a stream for it', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        const page = await mount(fetchMock, {
            search: '/',
            flowAccess: () => flowAccessFixture({ namespaces: [], lister: 'fails' }),
        });
        expect(streamCalls(fetchMock)).toHaveLength(0);

        const input = page.shadowRoot!.querySelector<HTMLInputElement>('#observed-ns-by-name')!;
        input.value = 'ns-unlisted';
        input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
        await page.updateComplete;

        expect(streamCalls(fetchMock)).toHaveLength(1);
        expect(String(streamCalls(fetchMock)[0][0])).toContain('observedNamespace=ns-unlisted');
        // Cleared, so the box does not sit there looking like the current scope - the select
        // below it is what shows that.
        expect(input.value).toBe('');
        expect(scopeValue(page)).toBe('ns-unlisted');
    });

    // replaceState changes the URL without notifying anyone, so a router reading the location
    // through its own abstraction keeps serving a stale value and builds links carrying a stale
    // scope, or none at all. The page says so with an event of its own, which the host router
    // adopts (see pages.test.tsx); without it the Service Map link silently dropped the scope.
    test('writing the scope to the URL tells the host', async () => {
        const page = await mount(async () => sseResponse([]), {
            search: '/?namespace=default',
            flowAccess: () => flowAccessFixture({
                namespaces: ['default', 'other'],
            }),
        });
        const seen: ScopeChangeDetail[] = [];
        page.addEventListener(SCOPE_CHANGE_EVENT, e => seen.push((e as CustomEvent<ScopeChangeDetail>).detail));

        await chooseScope(page, 'other');

        expect(seen).toEqual([{ search: '?namespace=other' }]);
    });

    // The event is the page's own, so nothing else is told about a navigation that did not
    // happen: popstate would reach every listener on the window.
    test('does not dispatch popstate', async () => {
        let pops = 0;
        const onPop = () => { pops += 1; };
        window.addEventListener('popstate', onPop);
        try {
            const page = await mount(async () => sseResponse([]), {
                search: '/',
                flowAccess: () => flowAccessFixture(),
            });
            await chooseScope(page, 'default');
            expect(pops).toBe(0);
        } finally {
            window.removeEventListener('popstate', onPop);
        }
    });

    // Writing the same URL again would tell the host for nothing, and a router that re-renders
    // on it would do so for every unrelated change.
    test('an unchanged URL does not tell the host', async () => {
        let events = 0;
        const onEvent = () => { events += 1; };
        // On the document, since the event is composed and so reaches here from inside the shadow
        // root.
        document.addEventListener(SCOPE_CHANGE_EVENT, onEvent);
        try {
            const page = await mount(async () => sseResponse([]), {
                search: '/?namespace=default',
                flowAccess: () => flowAccessFixture(),
            });
            // Picking the scope already in force writes the same URL again.
            await chooseScope(page, 'default');
            expect(events).toBe(0);
        } finally {
            document.removeEventListener(SCOPE_CHANGE_EVENT, onEvent);
        }
    });

    // A summary that could not be fetched is not a denial. A cluster-wide holder following a link
    // to their cluster scope must not lose it to a failed request, and has to be able to see it.
    test('a cluster scope in the URL is honoured when the answer is unknown', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        const page = await mount(fetchMock, {
            search: '/?namespace=_all',
            flowAccess: () => flowAccessFixture({ clusterWide: 'unknown' }),
        });

        const params = new URL(streamCalls(fetchMock)[0][0] as string, 'http://example.test').searchParams;
        expect(params.get('clusterWide')).toBe('true');
        expect(scopeValue(page)).toBe('__cluster_wide__');
    });

    // A grant that cannot be seen in the rules, as with a webhook authorizer, is not a denial: the
    // caller may choose the cluster scope, and the Flow Aggregator decides. It is not the default.
    test('the cluster scope is offered, but not the default, when the grant is unknown', async () => {
        const page = await mount(async () => sseResponse([]), {
            search: '/',
            flowAccess: () => flowAccessFixture({ namespaces: ['team-a'], clusterWide: 'unknown' }),
        });
        expect(scopeValue(page)).toBe('team-a');
        expect((await scopeMenu(page))[0]).toEqual({ value: '__cluster_wide__', text: 'All Namespaces' });
    });

    test('an unknown cluster-wide grant with no namespaces is not told there is nothing to observe', async () => {
        const page = await mount(async () => sseResponse([]), {
            search: '/',
            flowAccess: () => flowAccessFixture({ namespaces: [], clusterWide: 'unknown' }),
        });
        expect(page.shadowRoot!.textContent).not.toContain('not authorized to observe flows in any namespace');
        expect((await scopeMenu(page))[0]).toEqual({ value: '__cluster_wide__', text: 'All Namespaces' });
    });

    test('the free-text box is absent when the list is complete and not empty', async () => {
        // Not tidiness: if the list is complete, a namespace missing from it is one this user
        // most likely cannot observe, so the box could only invite a request that will be refused.
        const page = await mount(async () => sseResponse([]), {
            search: '/',
            flowAccess: () => flowAccessFixture(),
        });

        expect(page.shadowRoot!.querySelector('#observed-ns-by-name')).toBeNull();
    });

    describe('searching the namespaces', () => {
        const many = Array.from({ length: 25 }, (_, i) => `team-${String(i).padStart(2, '0')}`);
        const menu = async (page: AntreaFlowVisibilityPage) =>
            (await scopeMenu(page)).map(o => o.value).filter(v => v.startsWith('team-') || v === 'xray');

        test('a cluster admin is offered all namespaces, then the first ten, and can search the rest', async () => {
            const page = await mount(async () => sseResponse([]), {
                search: '/',
                flowAccess: () => flowAccessFixture({ clusterWide: 'allowed', lister: { names: [...many, 'xray'] } }),
            });
            expect(scopeValue(page)).toBe('__cluster_wide__');
            expect((await scopeMenu(page))[0]).toEqual({ value: '__cluster_wide__', text: 'All Namespaces' });
            expect(await menu(page)).toEqual(many.slice(0, 10));

            await typeScope(page, 'team-2');
            expect(await menu(page)).toEqual(many.slice(20));
            await typeScope(page, 'xr');
            expect(await menu(page)).toEqual(['xray']);
        });

        test('a user with more than ten candidates is shown the first ten', async () => {
            const page = await mount(async () => sseResponse([]), {
                search: '/',
                flowAccess: () => flowAccessFixture({ namespaces: many }),
            });
            expect(await menu(page)).toEqual(many.slice(0, 10));
            await typeScope(page, 'team-2');
            expect(await menu(page)).toEqual(many.filter(n => n.startsWith('team-2')).slice(0, 10));
        });

        // The options follow what is typed at once, so Enter acts on the current query rather than
        // the previous one.
        test('Enter right after retyping selects from the new query', async () => {
            const fetchMock = vi.fn(async () => sseResponse([]));
            const page = await mount(fetchMock, {
                search: '/?namespace=other',
                flowAccess: () => flowAccessFixture({ namespaces: ['dev', 'other', 'prod'] }),
            });
            const input = scopeInput(page);
            input.value = 'd';
            input.dispatchEvent(new Event('input', { bubbles: true }));
            input.value = 'p';
            input.dispatchEvent(new Event('input', { bubbles: true }));
            await pressInScope(page, 'Enter');
            expect(scopeValue(page)).toBe('prod');
        });

        test('says so when nothing matches', async () => {
            const page = await mount(async () => sseResponse([]), {
                search: '/',
                flowAccess: () => flowAccessFixture({ clusterWide: 'allowed', lister: { names: many } }),
            });
            await typeScope(page, 'nope');
            expect(page.shadowRoot!.querySelector('.combobox-empty')?.textContent).toContain('No matching');
        });

        test('shows the scope in force while closed and what is typed while open', async () => {
            const page = await mount(async () => sseResponse([]), {
                search: '/?namespace=team-03',
                flowAccess: () => flowAccessFixture({ clusterWide: 'allowed', lister: { names: many } }),
            });
            expect(scopeInput(page).value).toBe('team-03');
            await openScope(page);
            expect(scopeInput(page).value).toBe('');
        });

        test('arrow keys and Enter pick an option, and Escape closes without picking', async () => {
            const fetchMock = vi.fn(async () => sseResponse([]));
            const page = await mount(fetchMock, {
                search: '/?namespace=team-00',
                flowAccess: () => flowAccessFixture({ clusterWide: 'allowed', lister: { names: many } }),
            });
            await openScope(page);
            await pressInScope(page, 'ArrowDown');
            await pressInScope(page, 'ArrowDown');
            await pressInScope(page, 'Escape');
            expect(page.shadowRoot!.querySelector('#observed-ns-list')).toBeNull();
            expect(scopeValue(page)).toBe('team-00');

            await openScope(page);
            // The menu is "All Namespaces", team-00, team-01...: three steps reach the third.
            await pressInScope(page, 'ArrowDown');
            await pressInScope(page, 'ArrowDown');
            await pressInScope(page, 'ArrowDown');
            await pressInScope(page, 'Enter');
            await vi.advanceTimersByTimeAsync(0);
            expect(scopeValue(page)).toBe('team-01');
            expect(window.location.search).toContain('namespace=team-01');
        });

        test('ArrowUp with nothing highlighted starts at the last option', async () => {
            const page = await mount(async () => sseResponse([]), {
                search: '/?namespace=team-00',
                flowAccess: () => flowAccessFixture({ clusterWide: 'allowed', lister: { names: many } }),
            });
            await openScope(page);
            await pressInScope(page, 'ArrowUp');
            await pressInScope(page, 'Enter');
            await vi.advanceTimersByTimeAsync(0);
            // The menu is "All Namespaces", then the first ten: team-00 to team-09.
            expect(scopeValue(page)).toBe('team-09');
        });

        test('Enter on a single match picks it, and on nothing typed picks nothing', async () => {
            const page = await mount(async () => sseResponse([]), {
                search: '/?namespace=team-00',
                flowAccess: () => flowAccessFixture({ clusterWide: 'allowed', lister: { names: [...many, 'xray'] } }),
            });
            await openScope(page);
            await pressInScope(page, 'Enter');
            expect(scopeValue(page)).toBe('team-00');

            await typeScope(page, 'xr');
            await pressInScope(page, 'Enter');
            await vi.advanceTimersByTimeAsync(0);
            expect(scopeValue(page)).toBe('xray');
        });

        // The highlighted row indexed the list the menu showed, and means nothing once the menu
        // has closed: an Enter in the input, which keeps focus, must not pick from the unfiltered
        // list the next open would show.
        test('Enter after a pick, or after Escape, picks nothing more', async () => {
            const page = await mount(async () => sseResponse([]), {
                search: '/?namespace=team-00',
                flowAccess: () => flowAccessFixture({ clusterWide: 'allowed', lister: { names: many } }),
            });
            await typeScope(page, 'team-1');
            await pressInScope(page, 'ArrowDown');
            await pressInScope(page, 'ArrowDown');
            await pressInScope(page, 'Enter');
            await vi.advanceTimersByTimeAsync(0);
            expect(scopeValue(page)).toBe('team-11');

            await pressInScope(page, 'Enter');
            await vi.advanceTimersByTimeAsync(0);
            expect(scopeValue(page)).toBe('team-11');

            await openScope(page);
            await pressInScope(page, 'ArrowDown');
            await pressInScope(page, 'ArrowDown');
            await pressInScope(page, 'Escape');
            await pressInScope(page, 'Enter');
            await vi.advanceTimersByTimeAsync(0);
            expect(scopeValue(page)).toBe('team-11');
        });

        // A search only filters the list: the scope in force is added to the options only while
        // nothing is typed, so typing never offers a name the list does not hold.
        test('a selected namespace outside the list is offered only while nothing is typed', async () => {
            const page = await mount(async () => sseResponse([]), {
                search: '/?namespace=ns-x',
                flowAccess: () => flowAccessFixture({ clusterWide: 'allowed', lister: { names: many } }),
            });
            expect((await scopeMenu(page)).map(o => o.value)).toContain('ns-x');
            await typeScope(page, 'ns');
            expect((await scopeMenu(page)).map(o => o.value)).not.toContain('ns-x');
            await typeScope(page, 'ns-x');
            await pressInScope(page, 'Enter');
            await vi.advanceTimersByTimeAsync(0);
            expect(page.shadowRoot!.querySelector('.combobox-empty')?.textContent ?? '').toContain('No matching');
        });

        test('the options start from the whole list again after a pick or Escape', async () => {
            const page = await mount(async () => sseResponse([]), {
                search: '/?namespace=team-00',
                flowAccess: () => flowAccessFixture({ clusterWide: 'allowed', lister: { names: many } }),
            });
            await typeScope(page, 'team-1');
            expect((await scopeMenu(page)).map(o => o.value)).not.toContain('team-00');
            await pressInScope(page, 'Escape');
            expect((await scopeMenu(page)).map(o => o.value)).toContain('team-00');

            await typeScope(page, 'team-1');
            await pressInScope(page, 'ArrowDown');
            await pressInScope(page, 'Enter');
            await vi.advanceTimersByTimeAsync(0);
            expect(scopeValue(page)).toBe('team-10');
            expect((await scopeMenu(page)).map(o => o.value)).toContain('team-00');
        });

        // The name typed in full is the one meant, even when it is also the start of another.
        test('Enter on a name that starts another picks the name typed', async () => {
            const page = await mount(async () => sseResponse([]), {
                search: '/?namespace=zeta',
                flowAccess: () => flowAccessFixture({ clusterWide: 'allowed', lister: { names: ['app', 'app-staging', 'zeta'] } }),
            });
            await typeScope(page, 'app');
            await pressInScope(page, 'Enter');
            await vi.advanceTimersByTimeAsync(0);
            expect(scopeValue(page)).toBe('app');
        });

        test('a click outside closes the menu', async () => {
            const page = await mount(async () => sseResponse([]), {
                search: '/',
                flowAccess: () => flowAccessFixture({ clusterWide: 'allowed', lister: { names: many } }),
            });
            await openScope(page);
            expect(page.shadowRoot!.querySelector('#observed-ns-list')).not.toBeNull();
            document.body.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, composed: true }));
            window.dispatchEvent(new PointerEvent('pointerdown'));
            await page.updateComplete;
            expect(page.shadowRoot!.querySelector('#observed-ns-list')).toBeNull();
        });
    });

    // Only one menu is open at a time, whichever of them was used last.
    test('the scope menu and a peer filter menu close each other', async () => {
        const page = await mount(async () => sseResponse([]), {
            search: '/?namespace=ns-a',
            flowAccess: () => flowAccessFixture({ namespaces: ['ns-a', 'ns-b'] }),
        });
        const peerMenuOpen = () => page.shadowRoot!.querySelector('.multiselect-dropdown') !== null;
        const scopeMenuOpen = () => page.shadowRoot!.querySelector('#observed-ns-list') !== null;

        await openScope(page);
        expect(scopeMenuOpen()).toBe(true);
        page.shadowRoot!.querySelector<HTMLButtonElement>('.multiselect-btn')!.click();
        await page.updateComplete;
        expect(scopeMenuOpen()).toBe(false);
        expect(peerMenuOpen()).toBe(true);

        // A press on the scope input is outside the peer menu, which closes.
        scopeInput(page).dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, composed: true }));
        await openScope(page);
        expect(scopeMenuOpen()).toBe(true);
        expect(peerMenuOpen()).toBe(false);
    });

    test('an empty free-text box does nothing', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        const page = await mount(fetchMock, {
            search: '/',
            flowAccess: () => flowAccessFixture({ namespaces: [], lister: 'fails' }),
        });

        const input = page.shadowRoot!.querySelector<HTMLInputElement>('#observed-ns-by-name')!;
        input.value = '   ';
        input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
        await page.updateComplete;

        expect(streamCalls(fetchMock)).toHaveLength(0);
    });

    // The list can be incomplete, so a namespace missing from it is not proof the user cannot
    // observe it — a shared link naming one still has to work.
    test('a namespace the list does not mention is still honoured and shown as selected', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        const page = await mount(fetchMock, {
            search: '/?namespace=ns-unlisted',
            flowAccess: () => flowAccessFixture({ namespaces: [], lister: 'fails' }),
        });

        expect(streamCalls(fetchMock)).toHaveLength(1);
        expect(scopeValue(page)).toBe('ns-unlisted');
    });

    // No namespace is checked before it is selected, so none is left out or marked.
    test('a namespace the user cannot observe is offered like any other', async () => {
        const page = await mount(async () => sseResponse([]), {
            search: '/',
            flowAccess: () => flowAccessFixture({
                namespaces: ['default', 'locked'],
            }),
        });
        const values = (await scopeMenu(page)).map(o => o.value);
        expect(values).toContain('default');
        expect(values).toContain('locked');
    });
});

// The rules for the Namespace scope of a Namespace-aware page, as they apply to this one: where the
// scope comes from, what is written to the URL, how a typed name is treated, and what the page
// says when the list does not know the namespace.
describe('AntreaFlowVisibilityPage — the scope in the URL', () => {
    const streamParams = (fetchMock: { mock: { calls: unknown[][] } }, i = 0) =>
        new URL(streamCalls(fetchMock)[i][0] as string, 'http://example.test').searchParams;

    // Only a default needs the list and the verdict. A scope in the URL is used as given, so the
    // stream opens on it while the list is still loading for the picker.
    test('a scope in the URL does not wait for the access summary', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        let release!: () => void;
        const gate = new Promise<void>(r => { release = r; });
        resetAccessSummary();
        window.history.replaceState(null, '', '/?namespace=ns-a');
        vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
            if (typeof url === 'string' && url.includes('/api/v1/access-summary')) {
                await gate;
                return accessSummaryResponse(flowAccessFixture());
            }
            return fetchMock(url as never, init as never);
        }));
        el = document.createElement('antrea-flow-visibility-page') as AntreaFlowVisibilityPage;
        document.body.appendChild(el);
        await el.updateComplete;
        await vi.advanceTimersByTimeAsync(0);

        expect(streamCalls(fetchMock)).toHaveLength(1);
        expect(streamParams(fetchMock).get('observedNamespace')).toBe('ns-a');

        release();
        await vi.advanceTimersByTimeAsync(0);
        await el.updateComplete;
        expect(streamCalls(fetchMock)).toHaveLength(1);
        expect(scopeValue(el)).toBe('ns-a');
    });

    // The list is still loading when the user picks a scope, which All Namespaces allows since
    // nothing says yet that the user lacks it. The answer that arrives afterwards settles the
    // default's business only, and must read the page's own URL: the router has not delivered the
    // pick to `locationSearch` yet.
    test('a scope picked while the access summary loads is kept when it arrives', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        let release!: () => void;
        const gate = new Promise<void>(r => { release = r; });
        resetAccessSummary();
        window.history.replaceState(null, '', '/?namespace=ns-a');
        vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
            if (typeof url === 'string' && url.includes('/api/v1/access-summary')) {
                await gate;
                return accessSummaryResponse(flowAccessFixture({ namespaces: ['ns-a', 'ns-b'], clusterWide: 'denied' }));
            }
            return fetchMock(url as never, init as never);
        }));
        el = document.createElement('antrea-flow-visibility-page') as AntreaFlowVisibilityPage;
        el.locationSearch = '?namespace=ns-a';
        document.body.appendChild(el);
        await el.updateComplete;
        await vi.advanceTimersByTimeAsync(0);

        await chooseScope(el, '__cluster_wide__');
        await vi.advanceTimersByTimeAsync(0);
        expect(scopeValue(el)).toBe('__cluster_wide__');

        release();
        await vi.advanceTimersByTimeAsync(0);
        await el.updateComplete;
        expect(scopeValue(el)).toBe('__cluster_wide__');
        expect(window.location.search).toContain('namespace=_all');
        const calls = streamCalls(fetchMock);
        expect(new URL(calls[calls.length - 1][0] as string, 'http://example.test').searchParams.get('clusterWide')).toBe('true');
    });

    // The element is connected before the host attaches its listeners, so the scope the URL gave
    // at mount is told again once the page has rendered. A listener on the element itself, added
    // right after it is appended, only hears that second telling: a listener on the document
    // could not tell it from the first.
    test('tells the host the scope the URL gave once mounted', async () => {
        let release!: () => void;
        const gate = new Promise<void>(r => { release = r; });
        resetAccessSummary();
        window.history.replaceState(null, '', '/?namespace=ns-a');
        vi.stubGlobal('fetch', vi.fn(async (url: string) => {
            if (typeof url === 'string' && url.includes('/api/v1/access-summary')) {
                await gate;
                return accessSummaryResponse(flowAccessFixture());
            }
            return sseResponse([]);
        }));
        el = document.createElement('antrea-flow-visibility-page') as AntreaFlowVisibilityPage;
        document.body.appendChild(el);
        const seen: string[] = [];
        el.addEventListener(SCOPE_CHOSEN_EVENT, e => seen.push((e as CustomEvent<ScopeChosenDetail>).detail.scope));
        await el.updateComplete;

        expect(seen).toEqual(['ns-a']);
        release();
        await vi.advanceTimersByTimeAsync(0);
    });

    test('an explicit choice is written to the URL, and a default is not', async () => {
        // Unknown, so that All Namespaces is offered without being the default.
        const page = await mount(async () => sseResponse([]), {
            search: '/',
            flowAccess: () => flowAccessFixture({ namespaces: ['ns-a', 'ns-b'], clusterWide: 'unknown' }),
        });
        expect(window.location.search).toBe('');

        await chooseScope(page, 'ns-b');
        expect(window.location.search).toBe('?namespace=ns-b');

        await chooseScope(page, '__cluster_wide__');
        expect(window.location.search).toBe('?namespace=_all');
    });

    // Written in place: a history entry per pick would make Back walk through every one.
    test('writing the scope adds no history entry', async () => {
        const before = window.history.length;
        const page = await mount(async () => sseResponse([]), {
            search: '/',
            flowAccess: () => flowAccessFixture({ namespaces: ['ns-a', 'ns-b'] }),
        });
        await chooseScope(page, 'ns-b');
        expect(window.history.length).toBe(before);
    });

    // Choosing what the page was defaulted to is still a choice, and a reload keeps it.
    test('choosing the default scope writes it', async () => {
        const page = await mount(async () => sseResponse([]), {
            search: '/',
            flowAccess: () => flowAccessFixture({ namespaces: ['ns-a', 'ns-b'] }),
        });
        await chooseScope(page, 'ns-a');
        expect(window.location.search).toBe('?namespace=ns-a');
    });

    describe('an invalid scope', () => {
        test.each([
            ['an invalid name', '/?namespace=Not_Valid'],
            ['a name that is too long', `/?namespace=${'a'.repeat(64)}`],
            ['an empty value', '/?namespace='],
            ['a repeated parameter', '/?namespace=ns-a&namespace=ns-b'],
        ])('%s is reported, and nothing is selected', async (_name, search) => {
            const fetchMock = vi.fn(async () => sseResponse([]));
            const page = await mount(fetchMock, {
                search,
                flowAccess: () => flowAccessFixture({ namespaces: ['ns-a'], clusterWide: 'allowed' }),
            });

            // Not used, and not replaced by a default either.
            expect(streamCalls(fetchMock)).toHaveLength(0);
            expect(scopeValue(page)).toBe('');
            expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')).not.toBeNull();
        });

        test('the old parameters are not a scope', async () => {
            const fetchMock = vi.fn(async () => sseResponse([]));
            const page = await mount(fetchMock, {
                search: '/?observedNamespace=ns-a&clusterWide=true',
                flowAccess: () => flowAccessFixture({ namespaces: ['ns-b'] }),
            });
            // The URL has no scope, so the default order runs.
            expect(scopeValue(page)).toBe('ns-b');
        });

        test('picking a scope replaces the report', async () => {
            const page = await mount(async () => sseResponse([]), {
                search: '/?namespace=Not_Valid',
                flowAccess: () => flowAccessFixture({ namespaces: ['ns-a'] }),
            });
            await chooseScope(page, 'ns-a');
            expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')).toBeNull();
            expect(scopeValue(page)).toBe('ns-a');
        });
    });

    describe('naming a namespace', () => {
        test('a typed name that is not a valid namespace name is rejected', async () => {
            const fetchMock = vi.fn(async () => sseResponse([]));
            const page = await mount(fetchMock, {
                search: '/',
                flowAccess: () => flowAccessFixture({ namespaces: [], lister: 'fails' }),
            });
            const input = page.shadowRoot!.querySelector<HTMLInputElement>('#observed-ns-by-name')!;
            input.value = 'Not_Valid';
            input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
            await page.updateComplete;

            expect(streamCalls(fetchMock)).toHaveLength(0);
            expect(scopeValue(page)).toBe('');
            expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')).not.toBeNull();
        });

        // The value of All Namespaces is not a valid Namespace name, but it can be typed, in the box
        // or in the search. It is a name there, and so rejected: it must not select All Namespaces
        // for a user who is not offered it.
        test('the value of All Namespaces, typed as a name, is rejected', async () => {
            const fetchMock = vi.fn(async () => sseResponse([]));
            const page = await mount(fetchMock, {
                search: '/',
                flowAccess: () => flowAccessFixture({ namespaces: [], clusterWide: 'denied' }),
            });
            const input = page.shadowRoot!.querySelector<HTMLInputElement>('#observed-ns-by-name')!;
            input.value = '__cluster_wide__';
            input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
            await page.updateComplete;

            expect(streamCalls(fetchMock)).toHaveLength(0);
            expect(scopeValue(page)).toBe('');
            expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')).not.toBeNull();
            expect(window.location.search).toBe('');

            await openScope(page);
            await typeScope(page, '__cluster_wide__');
            await pressInScope(page, 'Enter');
            await page.updateComplete;

            expect(streamCalls(fetchMock)).toHaveLength(0);
            expect(scopeValue(page)).toBe('');
            expect(window.location.search).toBe('');
        });

        // A webhook authorizer may grant flows in a namespace no RoleBinding names, and the user
        // then has an empty list and no other way to reach it.
        test('an empty list with incomplete rules offers the box, and Enter on a typed name uses it', async () => {
            const fetchMock = vi.fn(async () => sseResponse([]));
            const page = await mount(fetchMock, {
                search: '/',
                flowAccess: () => flowAccessFixture({ namespaces: [], clusterWide: 'unknown' }),
            });
            expect(page.shadowRoot!.querySelector('#observed-ns-by-name')).not.toBeNull();
            expect(page.shadowRoot!.textContent).toContain('no complete list of namespaces');

            await openScope(page);
            await typeScope(page, 'ns-hook');
            await pressInScope(page, 'Enter');
            await vi.advanceTimersByTimeAsync(0);

            expect(streamCalls(fetchMock)).toHaveLength(1);
            expect(streamParams(fetchMock).get('observedNamespace')).toBe('ns-hook');
            expect(window.location.search).toBe('?namespace=ns-hook');
        });

        // Listing stops at a safeguard and a cut list counts like an unknown one.
        test('a cut list offers the box too', async () => {
            const page = await mount(async () => sseResponse([]), {
                search: '/',
                flowAccess: () => flowAccessFixture({ lister: { names: ['ns-a', 'ns-b'], cut: true } }),
            });
            expect(page.shadowRoot!.querySelector('#observed-ns-by-name')).not.toBeNull();
        });
    });

    describe('the menu', () => {
        const many = Array.from({ length: 25 }, (_, i) => `team-${String(i).padStart(2, '0')}`);

        test('says when more than ten match', async () => {
            const page = await mount(async () => sseResponse([]), {
                search: '/',
                flowAccess: () => flowAccessFixture({ clusterWide: 'allowed', lister: { names: many } }),
            });
            await openScope(page);
            const list = () => page.shadowRoot!.querySelector('#observed-ns-list')!.textContent;
            // Nothing typed: it is the first ten of the list, not the whole of it.
            expect(list()).toContain('Showing the first 10 - type to search');

            await typeScope(page, 'team-');
            expect(list()).toContain('More matches, keep typing');

            // Narrowed to what fits, it says no such thing.
            await typeScope(page, 'team-2');
            expect(list()).not.toContain('More matches');
            expect(list()).not.toContain('Showing the first 10');
        });

        test('hides All Namespaces while something is typed, and finds a name outside the first ten', async () => {
            const page = await mount(async () => sseResponse([]), {
                search: '/',
                flowAccess: () => flowAccessFixture({ clusterWide: 'allowed', lister: { names: many } }),
            });
            await openScope(page);
            await typeScope(page, ' TEAM-2 ');
            const values = Array.from(page.shadowRoot!.querySelectorAll<HTMLElement>('#observed-ns-list [role="option"]'))
                .map(o => o.dataset.value);
            expect(values).toEqual(['team-20', 'team-21', 'team-22', 'team-23', 'team-24']);
        });
    });

    describe('a namespace the list does not hold', () => {
        const note = (page: AntreaFlowVisibilityPage) =>
            page.shadowRoot!.querySelector('[data-testid="scope-note"]')?.textContent?.trim() ?? null;

        // The page still loads it: the list is a hint.
        test('a user who may not list namespaces is told it may not exist or be accessible', async () => {
            const fetchMock = vi.fn(async () => sseResponse([]));
            const page = await mount(fetchMock, {
                search: '/?namespace=ns-x',
                flowAccess: () => flowAccessFixture({ namespaces: ['ns-a'] }),
            });
            expect(streamCalls(fetchMock)).toHaveLength(1);
            expect(note(page)).toBe('Namespace ns-x is not in your list: it may not exist, or you may not have access');
        });

        test('a user who may list namespaces is told it does not exist once a fresh check says so', async () => {
            const page = await mount(async () => sseResponse([]), {
                search: '/?namespace=ns-typo',
                flowAccess: () => flowAccessFixture({ clusterWide: 'allowed', lister: { names: ['ns-a'] } }),
            });
            await vi.advanceTimersByTimeAsync(0);
            await page.updateComplete;
            expect(note(page)).toBe('Namespace ns-typo does not exist');
        });

        // Created since the list was loaded.
        test('no note when the fresh check finds it', async () => {
            const page = await mount(async () => sseResponse([]), {
                search: '/?namespace=ns-new',
                flowAccess: () => flowAccessFixture({
                    clusterWide: 'allowed', lister: { names: ['ns-a'] }, alsoExist: ['ns-new'],
                }),
            });
            await vi.advanceTimersByTimeAsync(0);
            await page.updateComplete;
            expect(note(page)).toBeNull();
        });

        test('no note when the namespace is listed', async () => {
            const page = await mount(async () => sseResponse([]), {
                search: '/?namespace=ns-a',
                flowAccess: () => flowAccessFixture({ namespaces: ['ns-a'] }),
            });
            expect(note(page)).toBeNull();
        });

        test.each([
            ['empty', { namespaces: [] as string[] }],
            ['unknown', { namespaces: [] as string[], lister: 'fails' as const }],
            ['cut', { lister: { names: ['ns-a'], cut: true } }],
        ])('no note when the list is %s', async (_name, fixture) => {
            const page = await mount(async () => sseResponse([]), {
                search: '/?namespace=ns-x',
                flowAccess: () => flowAccessFixture(fixture),
            });
            await vi.advanceTimersByTimeAsync(0);
            await page.updateComplete;
            expect(note(page)).toBeNull();
        });
    });

    describe('following the URL', () => {
        const navigate = async (page: AntreaFlowVisibilityPage, search: string) => {
            page.locationSearch = search;
            await page.updateComplete;
            await vi.advanceTimersByTimeAsync(0);
        };

        // Back and Forward, or switching between Flow List and Service Map, which does not remount.
        test('takes its scope from a new URL', async () => {
            const fetchMock = vi.fn(async () => sseResponse([]));
            const page = await mount(fetchMock, {
                search: '/?namespace=ns-a',
                flowAccess: () => flowAccessFixture({ namespaces: ['ns-a', 'ns-b'] }),
            });
            page.locationSearch = '?namespace=ns-a';
            await page.updateComplete;

            await navigate(page, '?namespace=ns-b');

            expect(scopeValue(page)).toBe('ns-b');
            expect(streamCalls(fetchMock)).toHaveLength(2);
            expect(streamParams(fetchMock, 1).get('observedNamespace')).toBe('ns-b');
        });

        test('runs the default order when the new URL has no scope', async () => {
            const page = await mount(async () => sseResponse([]), {
                search: '/?namespace=ns-b',
                flowAccess: () => flowAccessFixture({ namespaces: ['ns-a', 'ns-b'] }),
            });
            page.locationSearch = '?namespace=ns-b';
            await page.updateComplete;

            await navigate(page, '');

            expect(scopeValue(page)).toBe('ns-a');
        });

        // The host hands back what the page wrote: that is not a navigation.
        test('does not take its own write for a navigation', async () => {
            const fetchMock = vi.fn(async () => sseResponse([]));
            const page = await mount(fetchMock, {
                search: '/?namespace=ns-a',
                flowAccess: () => flowAccessFixture({ namespaces: ['ns-a', 'ns-b'] }),
            });
            page.locationSearch = '?namespace=ns-a';
            await page.updateComplete;

            // Taking the URL again would dispatch the scope again: only the pick may be seen.
            const seen: string[] = [];
            page.addEventListener(SCOPE_CHOSEN_EVENT, e => seen.push((e as CustomEvent<ScopeChosenDetail>).detail.scope));
            await chooseScope(page, 'ns-b');
            page.locationSearch = '?namespace=ns-b';
            await page.updateComplete;
            await vi.advanceTimersByTimeAsync(0);

            expect(seen).toEqual(['ns-b']);
            expect(streamCalls(fetchMock)).toHaveLength(2);
            expect(scopeValue(page)).toBe('ns-b');
        });
    });

    describe('the last scope chosen', () => {
        const chosen = async (setup: (page: AntreaFlowVisibilityPage) => Promise<void>, search: string) => {
            const seen: string[] = [];
            const onChosen = (e: Event) => seen.push((e as CustomEvent<ScopeChosenDetail>).detail.scope);
            document.addEventListener(SCOPE_CHOSEN_EVENT, onChosen);
            try {
                const page = await mount(async () => sseResponse([]), {
                    search,
                    flowAccess: () => flowAccessFixture({ namespaces: ['ns-a', 'ns-b'] }),
                });
                await setup(page);
            } finally {
                document.removeEventListener(SCOPE_CHOSEN_EVENT, onChosen);
            }
            return seen;
        };

        test('a pick and a scope from the URL count', async () => {
            expect(await chosen(async page => { await chooseScope(page, 'ns-b'); }, '/?namespace=ns-a'))
                .toContain('ns-b');
            expect(await chosen(async () => {}, '/?namespace=_all')).toContain('_all');
        });

        test('a default does not count', async () => {
            expect(await chosen(async () => {}, '/')).toEqual([]);
        });
    });
});

// A scope change starts a new stream, and nothing of the old one may be left on screen under the
// new scope.
describe('AntreaFlowVisibilityPage — changing the scope', () => {
    test('a refusal of the old scope is not shown over the new one', async () => {
        const fetchMock = vi.fn(async (url: string) =>
            url.includes('observedNamespace=ns-a') ? sseResponse([], 403) : sseResponse([]));
        const page = await mount(fetchMock, {
            search: '/?namespace=ns-a',
            flowAccess: () => flowAccessFixture({ namespaces: ['ns-a', 'ns-b'] }),
        });
        await vi.advanceTimersByTimeAsync(0);
        await page.updateComplete;
        expect(page.shadowRoot!.textContent).toContain('not authorized to observe flows in this scope');

        // Paused, so that no stream connects to clear it: the banner went with the stream.
        (page as unknown as { _paused: boolean })._paused = true;
        await chooseScope(page, 'ns-b');
        await vi.advanceTimersByTimeAsync(0);
        await page.updateComplete;

        expect(page.shadowRoot!.textContent).not.toContain('not authorized to observe flows in this scope');
    });

    // Stopping a client flushes its pending batch, which must not land in a store that was just
    // cleared for the new scope.
    test('records still pending for the old scope do not appear under the new one', async () => {
        const fetchMock = vi.fn(async (url: string) => url.includes('observedNamespace=ns-a')
            ? sseResponse([flowEventChunk([makeFlow({ srcNs: 'ns-a', srcPod: 'old-pod' })])])
            : sseResponse([]));
        const page = await mount(fetchMock, {
            search: '/?namespace=ns-a',
            flowAccess: () => flowAccessFixture({ namespaces: ['ns-a', 'ns-b'] }),
        });
        // Received, and still inside the batch interval.
        await vi.advanceTimersByTimeAsync(10);

        await chooseScope(page, 'ns-b');
        await vi.advanceTimersByTimeAsync(2000);
        await page.updateComplete;

        expect(page.shadowRoot!.textContent).not.toContain('old-pod');
    });

    // Picked while the access loads, where All Namespaces is offered: the URL then names it and is
    // used as given, so the page is on a scope that gets the Flow Aggregator's answer, and the
    // two scope fields do not disagree.
    test('exactly one scope is in force', async () => {
        const page = await mount(async () => sseResponse([]), {
            search: '/?namespace=ns-a',
            flowAccess: () => flowAccessFixture({ clusterWide: 'allowed', namespaces: ['ns-a'] }),
        });
        const priv = page as unknown as { _clusterWide: boolean; _observedNs: string };
        await chooseScope(page, '__cluster_wide__');
        expect([priv._clusterWide, priv._observedNs]).toEqual([true, '']);
        await chooseScope(page, 'ns-a');
        expect([priv._clusterWide, priv._observedNs]).toEqual([false, 'ns-a']);
    });
});

describe('AntreaFlowVisibilityPage — smoke and stream lifecycle', () => {
    // There is no credential to wait for: the browser sends the session cookie itself, so the
    // page opens the stream as soon as it knows the feature is enabled.
    test('the stream starts on connect and receives batched flows', async () => {
        const fetchMock = vi.fn(async () => sseResponse([flowEventChunk([makeFlow()])]));
        const page = await mount(fetchMock);
        await vi.advanceTimersByTimeAsync(0);
        await vi.advanceTimersByTimeAsync(1000); // default FlowStreamClient batch interval

        expect(streamCalls(fetchMock)).toHaveLength(1);
        expect(page.shadowRoot).not.toBeNull();
    });

    test('a page detached and attached again reopens its stream', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        const page = await mount(fetchMock);
        await vi.advanceTimersByTimeAsync(0);
        expect(streamCalls(fetchMock)).toHaveLength(1);

        const parent = page.parentElement!;
        page.remove();
        parent.appendChild(page);
        await vi.advanceTimersByTimeAsync(0);

        expect(streamCalls(fetchMock)).toHaveLength(2);
    });

    test('a stream 401 dispatches antrea-session-expired', async () => {
        // The stream opens from connectedCallback(), so the 401 can land before mount() returns.
        // Listen on document.body (the event bubbles and is composed) to catch it either way.
        const onSessionExpired = vi.fn();
        document.body.addEventListener('antrea-session-expired', onSessionExpired);
        try {
            await mount(async () => sseResponse([], 401));
            await vi.advanceTimersByTimeAsync(0);
            expect(onSessionExpired).toHaveBeenCalledTimes(1);
        } finally {
            document.body.removeEventListener('antrea-session-expired', onSessionExpired);
        }
    });

    // A 401 is terminal now: there is no token for the host to refresh, so nothing should try
    // the stream again. The host logs the user out instead.
    test('a 401 does not retry the stream', async () => {
        const fetchMock = vi.fn(async () => sseResponse([], 401));
        await mount(fetchMock);
        await vi.advanceTimersByTimeAsync(0);
        expect(streamCalls(fetchMock)).toHaveLength(1);

        await vi.advanceTimersByTimeAsync(60_000);
        expect(streamCalls(fetchMock)).toHaveLength(1);
    });
});

describe('AntreaFlowVisibilityPage — viewMode', () => {
    // There is no in-page control for this any more (see nav.tsx's antrea-nav-group): the host
    // drives it entirely from the URL, one-way.
    test('defaults to list view, renders the map and updates the page title when viewMode is set to "map"', async () => {
        const page = await mount(async () => sseResponse([]));
        await vi.advanceTimersByTimeAsync(0);
        expect(page.shadowRoot!.querySelector('#graph-svg')).toBeNull();
        expect(page.shadowRoot!.querySelector('.page-title')!.textContent).toBe('Flow List');

        page.viewMode = 'map';
        await page.updateComplete;

        expect(page.shadowRoot!.querySelector('#graph-svg')).not.toBeNull();
        expect(page.shadowRoot!.querySelector('.page-title')!.textContent).toBe('Service Map');
    });
});

describe('AntreaFlowVisibilityPage — antrea-edge-selected extension point', () => {
    async function mountWithOneEdge(): Promise<AntreaFlowVisibilityPage> {
        const page = await mount(async () => sseResponse([flowEventChunk([makeFlow({ ingressPolicy: 'allow-client' })])]));
        await page.updateComplete;
        await vi.advanceTimersByTimeAsync(0);
        await vi.advanceTimersByTimeAsync(1000);
        // Switch to map view — this is what triggers _buildServiceMap(), synchronously
        // populating _graphRef.edgeMap and wiring up click handlers on the rendered paths
        // (independent of the D3 force simulation, which only animates positions afterward).
        page.viewMode = 'map';
        await page.updateComplete;
        return page;
    }

    test('clicking an edge fires antrea-edge-selected with the edge details', async () => {
        const page = await mountWithOneEdge();
        const onSelected = vi.fn();
        page.addEventListener('antrea-edge-selected', onSelected);

        const path = page.shadowRoot!.querySelector<SVGPathElement>('svg path[fill="none"]');
        expect(path).not.toBeNull();
        path!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
        await page.updateComplete;

        expect(onSelected).toHaveBeenCalledTimes(1);
        const detail = onSelected.mock.calls[0][0].detail;
        expect(detail).toMatchObject({ protected: true, ingressPolicyNames: ['allow-client'] });
    });

    test('clicking the close button emits null', async () => {
        const page = await mountWithOneEdge();
        const path = page.shadowRoot!.querySelector<SVGPathElement>('svg path[fill="none"]')!;
        path.dispatchEvent(new MouseEvent('click', { bubbles: true }));
        await page.updateComplete;

        const onSelected = vi.fn();
        page.addEventListener('antrea-edge-selected', onSelected);
        const closeButton = page.shadowRoot!.querySelector<HTMLButtonElement>('.edge-details-close')!;
        closeButton.click();
        await page.updateComplete;

        expect(onSelected).toHaveBeenCalledTimes(1);
        expect(onSelected.mock.calls[0][0].detail).toBeNull();
    });

    test('emits null when the selected edge disappears on a topology rebuild', async () => {
        const page = await mountWithOneEdge();
        const path = page.shadowRoot!.querySelector<SVGPathElement>('svg path[fill="none"]')!;
        path.dispatchEvent(new MouseEvent('click', { bubbles: true }));
        await page.updateComplete;
        expect(page.shadowRoot!.querySelector('.edge-details-close')).not.toBeNull();

        const onSelected = vi.fn();
        page.addEventListener('antrea-edge-selected', onSelected);

        // Force a topology rebuild with a completely different edge (simulating the old one
        // aging out of the store) by replacing _entries directly.
        (page as unknown as { _entries: unknown[] })._entries = [];
        (page as unknown as { _buildServiceMap(): void })._buildServiceMap();
        await page.updateComplete;

        expect(onSelected).toHaveBeenCalledTimes(1);
        expect(onSelected.mock.calls[0][0].detail).toBeNull();
        expect(page.shadowRoot!.querySelector('.edge-details-close')).toBeNull();
    });
});

describe('AntreaFlowVisibilityPage — filters, sort, text filter, pause/resume, clear', () => {
    async function mountWithTwoFlows(fetchMock = vi.fn(async () => sseResponse([
        flowEventChunk([
            makeFlow({ srcPod: 'aaa-abc12', srcIP: '10.0.0.1' }),
            makeFlow({ srcPod: 'zzz-xyz34', srcIP: '10.0.0.2' }),
        ]),
    ]))): Promise<{ page: AntreaFlowVisibilityPage; fetchMock: typeof fetchMock }> {
        const page = await mount(fetchMock, { search: '/?namespace=ns-a' });
        // The SSE reader needs several microtask turns to drain the chunk, and the client only
        // hands flows over on its batch interval. Pump until the rows show up rather than
        // guessing a fixed number of turns.
        for (let i = 0; i < 5 && page.shadowRoot!.querySelectorAll('tbody tr').length < 2; i++) {
            await vi.advanceTimersByTimeAsync(1000);
            await page.updateComplete;
        }
        return { page, fetchMock };
    }

    test('applying a namespace filter restarts the stream with the new filter encoded', async () => {
        const { page, fetchMock } = await mountWithTwoFlows();
        expect(streamCalls(fetchMock)).toHaveLength(1);

        const nsToggle = page.shadowRoot!.querySelector<HTMLButtonElement>('.multiselect-btn')!;
        nsToggle.click();
        await page.updateComplete;
        toggleOption(page, 'default');
        await page.updateComplete;

        page.shadowRoot!.querySelector<HTMLElement>('.filter-actions antrea-button')!.click();
        await page.updateComplete;
        await vi.advanceTimersByTimeAsync(0);

        expect(streamCalls(fetchMock)).toHaveLength(2);
        expect(streamCalls(fetchMock)[1][0]).toContain('namespaces=default');
    });

    test('resetting filters restarts the stream with no filter', async () => {
        const { page, fetchMock } = await mountWithTwoFlows();

        const nsToggle = page.shadowRoot!.querySelector<HTMLButtonElement>('.multiselect-btn')!;
        nsToggle.click();
        await page.updateComplete;
        toggleOption(page, 'default');
        await page.updateComplete;
        const [applyBtn, resetBtn] = page.shadowRoot!.querySelectorAll<HTMLElement>('.filter-actions antrea-button');
        applyBtn.click();
        await page.updateComplete;
        await vi.advanceTimersByTimeAsync(0);
        expect(streamCalls(fetchMock)).toHaveLength(2);

        resetBtn.click();
        await page.updateComplete;
        await vi.advanceTimersByTimeAsync(0);

        expect(streamCalls(fetchMock)).toHaveLength(3);
        expect(streamCalls(fetchMock)[2][0]).not.toContain('namespaces');
    });

    test('clicking a column header sorts the flow list, toggling direction on a second click', async () => {
        const { page } = await mountWithTwoFlows();
        const sourceHeader = Array.from(page.shadowRoot!.querySelectorAll('th')).find(th => th.textContent?.includes('Source'))!;

        sourceHeader.click();
        await page.updateComplete;
        let rows = page.shadowRoot!.querySelectorAll('tbody tr');
        expect(rows[0].textContent).toContain('aaa-abc12');

        sourceHeader.click();
        await page.updateComplete;
        rows = page.shadowRoot!.querySelectorAll('tbody tr');
        expect(rows[0].textContent).toContain('zzz-xyz34');
    });

    /** Mounts the flows, then returns a function that sorts by a column and reads the order the
     * rows come out in, by a marker each flow carries in a field the column does not show. */
    async function sortedBy(flows: Flow[], header: string, markers: string[]): Promise<string[]> {
        const page = await mount(vi.fn(async () => sseResponse([flowEventChunk(flows)])), { search: '/?namespace=ns-a' });
        for (let i = 0; i < 5 && page.shadowRoot!.querySelectorAll('tbody tr').length < flows.length; i++) {
            await vi.advanceTimersByTimeAsync(1000);
            await page.updateComplete;
        }
        Array.from(page.shadowRoot!.querySelectorAll('th')).find(th => th.textContent?.includes(header))!.click();
        await page.updateComplete;
        return Array.from(page.shadowRoot!.querySelectorAll('tbody tr'))
            .map(r => markers.find(m => r.textContent!.includes(m))!);
    }

    // The cell of a withheld endpoint reads "ns-z/<hidden>", so that is where it sorts: not by
    // the address it also carries, which is not the text on screen.
    test('Source sorts by what the cell shows for a withheld endpoint', async () => {
        const flows = [
            makeFlow({ srcNs: 'ns-z', srcPod: '', srcIP: '10.0.0.1', srcDisclosure: EndpointDisclosure.Flow, dstPod: 'm-hidden' }),
            makeFlow({ srcNs: 'ns-a', srcPod: 'x', srcIP: '10.0.0.2', dstPod: 'm-first' }),
            makeFlow({ srcNs: 'ns-z', srcPod: 'zzz', srcIP: '10.0.0.3', dstPod: 'm-last' }),
        ];
        expect(await sortedBy(flows, 'Source', ['m-hidden', 'm-first', 'm-last']))
            .toEqual(['m-first', 'm-hidden', 'm-last']);
    });

    test('Destination sorts by what the cell shows for a withheld endpoint', async () => {
        const flows = [
            makeFlow({ srcPod: 'm-hidden', srcIP: '10.0.0.1', dstNs: 'ns-z', dstPod: '', dstIP: '10.1.0.9', dstDisclosure: EndpointDisclosure.Flow }),
            makeFlow({ srcPod: 'm-first', srcIP: '10.0.0.2', dstNs: 'ns-a', dstPod: 'x', dstIP: '10.1.0.2' }),
            makeFlow({ srcPod: 'm-last', srcIP: '10.0.0.3', dstNs: 'ns-z', dstPod: 'zzz', dstIP: '10.1.0.3' }),
        ];
        expect(await sortedBy(flows, 'Destination', ['m-hidden', 'm-first', 'm-last']))
            .toEqual(['m-first', 'm-hidden', 'm-last']);
    });

    // A withheld policy shows its action and a policy that never matched shows "-", and neither
    // is the empty name both used to sort as.
    test('the policy columns sort by what the cell shows', async () => {
        const flows = [
            makeFlow({ srcPod: 'm-hidden', srcIP: '10.0.0.1', dstDisclosure: EndpointDisclosure.Flow, ingressAction: NetworkPolicyRuleAction.Drop }),
            makeFlow({ srcPod: 'm-none', srcIP: '10.0.0.2' }),
            makeFlow({ srcPod: 'm-named', srcIP: '10.0.0.3', ingressPolicy: 'bbb-policy' }),
        ];
        expect(await sortedBy(flows, 'Ingress Policy', ['m-hidden', 'm-none', 'm-named']))
            .toEqual(['m-none', 'm-named', 'm-hidden']);
    });

    // A withheld Service has no text and an absent one reads "-": two cells, so two sort keys.
    test('Dest Service sorts a withheld Service apart from an absent one', async () => {
        const flows = [
            makeFlow({ srcPod: 'm-absent', srcIP: '10.0.0.1' }),
            makeFlow({ srcPod: 'm-withheld', srcIP: '10.0.0.2', svcDisclosure: EndpointDisclosure.Flow }),
            makeFlow({ srcPod: 'm-named', srcIP: '10.0.0.3', destService: 'ns-a/svc:http' }),
        ];
        expect(await sortedBy(flows, 'Dest Service', ['m-absent', 'm-withheld', 'm-named']))
            .toEqual(['m-withheld', 'm-absent', 'm-named']);
    });

    test('the text filter narrows the flow list to matching entries', async () => {
        const { page } = await mountWithTwoFlows();
        const input = page.shadowRoot!.querySelector<HTMLInputElement>('.flow-filter-input')!;

        input.value = 'zzz-xyz34';
        input.dispatchEvent(new Event('input', { bubbles: true }));
        await page.updateComplete;

        const rows = page.shadowRoot!.querySelectorAll('tbody tr');
        expect(rows).toHaveLength(1);
        expect(rows[0].textContent).toContain('zzz-xyz34');
    });

    test('pause stops the stream and resume restarts it', async () => {
        const { page, fetchMock } = await mountWithTwoFlows();
        const [, , pauseBtn] = page.shadowRoot!.querySelectorAll<HTMLElement>('.filter-actions antrea-button');

        pauseBtn.click();
        await page.updateComplete;
        expect(page.shadowRoot!.textContent).toContain('Paused');
        expect(pauseBtn.textContent).toContain('Resume');

        const callsAfterPause = streamCalls(fetchMock).length;
        await vi.advanceTimersByTimeAsync(60_000);
        expect(streamCalls(fetchMock)).toHaveLength(callsAfterPause);

        pauseBtn.click();
        await page.updateComplete;
        await vi.advanceTimersByTimeAsync(0);
        expect(streamCalls(fetchMock).length).toBeGreaterThan(callsAfterPause);
    });

    test('clear empties the flow list and resets counters', async () => {
        const { page } = await mountWithTwoFlows();
        expect(page.shadowRoot!.querySelectorAll('tbody tr')).toHaveLength(2);
        const [, , , clearBtn] = page.shadowRoot!.querySelectorAll<HTMLElement>('.filter-actions antrea-button');

        clearBtn.click();
        await page.updateComplete;

        expect(page.shadowRoot!.querySelectorAll('tbody tr')).toHaveLength(0);
        expect(page.shadowRoot!.textContent).toContain('0 connections');
    });
});

describe('AntreaFlowVisibilityPage — multiselect dropdown', () => {
    test('opens on click, toggles a selection, and closes on an outside click', async () => {
        const page = await mount(async () => sseResponse([flowEventChunk([makeFlow()])]), { search: '/?namespace=ns-a' });
        await page.updateComplete;
        await vi.advanceTimersByTimeAsync(1000);

        const toggle = page.shadowRoot!.querySelector<HTMLButtonElement>('.multiselect-btn')!;
        expect(page.shadowRoot!.querySelector('.multiselect-dropdown')).toBeNull();

        toggle.click();
        await page.updateComplete;
        expect(page.shadowRoot!.querySelector('.multiselect-dropdown')).not.toBeNull();
        expect(toggle.textContent).toContain('All');

        toggleOption(page, 'default');
        await page.updateComplete;
        expect(toggle.textContent).toContain('default');

        // _handleDocClick closes open dropdowns on any pointerdown outside .multiselect.
        document.body.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }));
        await page.updateComplete;
        expect(page.shadowRoot!.querySelector('.multiselect-dropdown')).toBeNull();
    });
});

describe('AntreaFlowVisibilityPage — request not served by this deployment', () => {
    const notServed = () => jsonResponse({ message: 'this deployment does not serve live streams', code: 'flow_source_not_served', retryable: false }, 501);

    // The backend answers 501 when no configured source serves the requested mode. Like a 400,
    // it stops that request and shows the backend's message, without retrying.
    test('a stream 501 shows the backend message and does not retry the stream', async () => {
        const fetchMock = vi.fn(async () => notServed());
        const page = await mount(fetchMock);
        await vi.advanceTimersByTimeAsync(0);

        expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')?.textContent)
            .toContain('does not serve live streams');

        expect(streamCalls(fetchMock)).toHaveLength(1);
        await vi.advanceTimersByTimeAsync(60_000);
        expect(streamCalls(fetchMock)).toHaveLength(1);
    });

    test('a 501 on a live request does not stop later finite queries', async () => {
        const fetchMock = vi.fn(async (url: string) => streamParams([url]).get('follow') === 'false'
            ? finiteResponse([flowEventChunk([makeFlow()]), FINITE_COMPLETE])
            : notServed());
        const page = await mount(fetchMock);
        await vi.advanceTimersByTimeAsync(0);
        await setQuery(page, { follow: false });

        expect(streamCalls(fetchMock)).toHaveLength(2);
        expect((page as unknown as { _queryState: string })._queryState).toBe('complete');
    });
});

// A retryable failure is retried indefinitely, so its disconnect resolves itself once the Flow
// Aggregator or the network recovers; a terminal one never will. The status must tell them apart.
describe('AntreaFlowVisibilityPage — stream status', () => {
    const statusText = (page: AntreaFlowVisibilityPage) =>
        page.shadowRoot!.querySelector('.status-row')?.textContent?.trim();

    test('shows Reconnecting while a retry is scheduled, then Connected once it succeeds', async () => {
        let calls = 0;
        const fetchMock = vi.fn(async (url: string) => {
            if (!url.includes('/api/v1/flows/stream')) return jsonResponse({});
            if (++calls === 1) throw new Error('network down');
            return sseResponse([]);
        });
        const page = await mount(fetchMock);
        await vi.advanceTimersByTimeAsync(0);
        await page.updateComplete;
        expect(statusText(page)).toContain('Reconnecting');

        await vi.advanceTimersByTimeAsync(1000);
        await page.updateComplete;
        expect(statusText(page)).toContain('Connected');
    });

    // The first attempt fails retryably so Reconnecting is showing when the retry hits the
    // terminal answer: only onDisconnected clearing it gets the page to Disconnected, since a
    // terminal failure fires no onReconnectScheduled.
    test.each([
        ['501', 501],
        ['403', 403],
    ])('shows Disconnected, not Reconnecting, once a retry hits a terminal %s', async (_name, status) => {
        let calls = 0;
        const fetchMock = vi.fn(async (url: string) => {
            if (!url.includes('/api/v1/flows/stream')) return jsonResponse({});
            if (++calls === 1) throw new Error('network down');
            // A closed body, as an error response has: the client reads it for the error payload.
            return status === 501 ? new Response('', { status }) : sseResponse([], status);
        });
        const page = await mount(fetchMock);
        await vi.advanceTimersByTimeAsync(0);
        await page.updateComplete;
        expect(statusText(page)).toContain('Reconnecting');

        await vi.advanceTimersByTimeAsync(1000);
        await page.updateComplete;
        expect(statusText(page)).toContain('Disconnected');
        expect(statusText(page)).not.toContain('Reconnecting');
    });
});

// Since nothing on the frontend gates access to this page
// anymore (authorization is entirely FA's), a real per-scope denial has to render a terminal panel
// naming the restriction on its own, rather than leaving the page retrying or showing nothing.
describe('AntreaFlowVisibilityPage — forbidden', () => {
    test('a stream 403 shows the restriction message and does not retry the stream', async () => {
        const fetchMock = vi.fn(async () => sseResponse([], 403));
        const page = await mount(fetchMock);
        await vi.advanceTimersByTimeAsync(0);

        expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')?.textContent)
            .toContain('not authorized to observe flows in this scope');
        // The docs it points to are a link, not a path to look up.
        expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"] a')?.getAttribute('href'))
            .toBe('https://github.com/antrea-io/antrea-ui/blob/main/docs/authentication.md#flow-data-is-per-user');

        expect(streamCalls(fetchMock)).toHaveLength(1);
        await vi.advanceTimersByTimeAsync(60_000);
        expect(streamCalls(fetchMock)).toHaveLength(1);
    });

    // The 403 survives a stream restart for the same request, not just the one open stream: every
    // user action that restarts the client goes through _startStream, whose only defence is
    // comparing _filterKey against _forbiddenFilterKey. A handler that cleared _client but left
    // that field unset would silently reopen the stream on the next interaction and 403 again.
    //
    // Driven through the real Pause/Resume buttons rather than a synthetic event: filters and the
    // pause toggle are plain @click handlers on the toolbar, so there is no event to dispatch, and
    // a test that invented one would assert nothing. Resume is the cheapest of them — _applyFilter
    // additionally early-returns on an unchanged filter key, so an "Apply Filters" click with no
    // pending change would not restart the stream even without the guard.
    test('the restriction survives a stream restart', async () => {
        const fetchMock = vi.fn(async () => sseResponse([], 403));
        const page = await mount(fetchMock);
        await vi.advanceTimersByTimeAsync(0);
        expect(streamCalls(fetchMock)).toHaveLength(1);

        const toggle = () => Array.from(page.shadowRoot!.querySelectorAll('antrea-button'))
            .find(b => b.textContent?.trim() === 'Pause' || b.textContent?.trim() === 'Resume')!;
        expect(toggle()).toBeDefined();
        toggle().dispatchEvent(new MouseEvent('click', { bubbles: true, composed: true }));
        await page.updateComplete;
        toggle().dispatchEvent(new MouseEvent('click', { bubbles: true, composed: true }));
        await page.updateComplete;
        await vi.advanceTimersByTimeAsync(60_000);

        expect(streamCalls(fetchMock)).toHaveLength(1);
        expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')?.textContent)
            .toContain('not authorized to observe flows in this scope');
    });

    // Authorization is per-scope, not per-page: a 403 for one request must not block a later,
    // different request from ever being tried. A peer-filter change is enough to prove the guard
    // is keyed to the request that was refused rather than latched for the page's lifetime -
    // _applyFilter's own early return on an unchanged filter key would otherwise make this
    // indistinguishable from "the restriction survives a stream restart" above.
    test('a filter change after a 403 retries with the new request', async () => {
        const fetchMock = vi.fn(async () => sseResponse([], 403));
        const page = await mount(fetchMock);
        await vi.advanceTimersByTimeAsync(0);
        expect(streamCalls(fetchMock)).toHaveLength(1);

        (page as unknown as { _applyFilter(filter: FlowStreamFilter): void })
            ._applyFilter({ namespaces: ['ns-a'] });
        await page.updateComplete;
        await vi.advanceTimersByTimeAsync(0);

        expect(streamCalls(fetchMock)).toHaveLength(2);
    });
});

// A 403 is about the scope that was asked for. Coming back to one after visiting another is a new
// request, and has to be answered as one: the refusal is shown again if it still stands, and a
// grant added in the meantime is picked up without reloading the page.
describe('AntreaFlowVisibilityPage — returning to a refused scope', () => {
    const selectScope = chooseScope;

    test('asks again, and shows the refusal again', async () => {
        const fetchMock = vi.fn(async (url: string) =>
            url.includes('observedNamespace=ns-a') ? sseResponse([], 403) : sseResponse([]));
        const page = await mount(fetchMock, {
            search: '/?namespace=ns-a',
            flowAccess: () => flowAccessFixture({
                namespaces: ['ns-a', 'ns-b'],
            }),
        });
        await vi.advanceTimersByTimeAsync(0);
        expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')?.textContent)
            .toContain('not authorized to observe flows in this scope');

        // A scope that connects clears the banner, which is what made the next step silent.
        await selectScope(page, 'ns-b');
        await page.updateComplete;
        await vi.advanceTimersByTimeAsync(0);
        await page.updateComplete;
        expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')).toBeNull();

        await selectScope(page, 'ns-a');
        await page.updateComplete;
        await vi.advanceTimersByTimeAsync(0);
        await page.updateComplete;

        expect(streamCalls(fetchMock)).toHaveLength(3);
        expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')?.textContent)
            .toContain('not authorized to observe flows in this scope');
    });
});

// The Namespaces multi-select is a filter, not a second scope control: it selects flows by an
// endpoint's namespace, which upstream explicitly supports naming an out-of-scope namespace for.
describe('AntreaFlowVisibilityPage — Namespaces filter', () => {
    async function mountWithPeers(search = '/?namespace=ns-a'): Promise<AntreaFlowVisibilityPage> {
        const flows = [
            makeFlow({ srcPod: 'a-abc12', srcIP: '10.0.0.1' }),
            makeFlow({ srcPod: 'b-xyz34', srcIP: '10.0.0.2', dstIP: '10.0.0.3', dstNs: 'kube-system' }),
        ];
        const page = await mount(async () => sseResponse([flowEventChunk(flows)]), { search });
        for (let i = 0; i < 5 && page.shadowRoot!.querySelectorAll('tbody tr').length < 2; i++) {
            await vi.advanceTimersByTimeAsync(1000);
            await page.updateComplete;
        }
        page.shadowRoot!.querySelector<HTMLButtonElement>('.multiselect-btn')!.click();
        await page.updateComplete;
        return page;
    }

    // The menu is built from the flows received so far and is not re-checked against the
    // namespaces this user may read Kubernetes objects in: that is a different grant, and after
    // per-user redaction a namespace can only appear on a record the Flow Aggregator already
    // decided this user may identify.
    test('offers every namespace seen in flows', async () => {
        const page = await mountWithPeers();
        expect(menuOptions(page)).toEqual(['default', 'kube-system']);
        expect(page.shadowRoot!.querySelector('.multiselect-hint')!.textContent)
            .toContain('seen in flows in this scope');
    });

    // With the endpoint on the source or the destination, the observed namespace selects the
    // traffic leaving or entering it.
    test('the observed namespace is offered too', async () => {
        const page = await mountWithPeers('/?namespace=default');
        expect(menuOptions(page)).toContain('default');
    });

    // The Flow Aggregator places such a destination in its Service's namespace, so a filter on
    // that namespace selects it.
    test('offers the Service namespace of a destination without a Pod namespace', async () => {
        const flows = [makeFlow({
            srcNs: 'ns-a', srcPod: 'client', srcIP: '10.0.0.1',
            dstNs: '', dstPod: '', dstIP: '172.19.0.3', destService: 'default/kubernetes:https',
        })];
        const page = await mount(async () => sseResponse([flowEventChunk(flows)]), { search: '/?namespace=ns-a' });
        for (let i = 0; i < 5 && page.shadowRoot!.querySelectorAll('tbody tr').length < 1; i++) {
            await vi.advanceTimersByTimeAsync(1000);
            await page.updateComplete;
        }
        page.shadowRoot!.querySelector<HTMLButtonElement>('.multiselect-btn')!.click();
        await page.updateComplete;
        expect(menuOptions(page)).toContain('default');
    });

    test('a selection stays in the menu once no name seen holds it', async () => {
        const page = await mountWithPeers();
        toggleOption(page, 'kube-system');
        await page.updateComplete;

        (page as unknown as { _seenNames: SeenNames })._seenNames.reset();
        // Reopened: the rows of an open menu do not change.
        const button = page.shadowRoot!.querySelector<HTMLButtonElement>('.multiselect-btn')!;
        button.click();
        await page.updateComplete;
        button.click();
        await page.updateComplete;
        expect(menuOptions(page)).toEqual(['kube-system']);
    });

    test('it is labelled Namespaces, in both views', async () => {
        const page = await mountWithPeers();
        const label = () => Array.from(page.shadowRoot!.querySelectorAll('.multiselect-label'))
            .map(el => el.textContent!.trim());
        expect(label()).toContain('Namespaces');

        page.viewMode = 'map';
        await page.updateComplete;
        expect(label()).toContain('Namespaces');
    });

    // The Flow Aggregator matches the endpoint filters against a single endpoint. Grouping them
    // under Endpoint is what sets this Namespaces filter apart from the scope.
    test('it sits in the Endpoint group, which says the group matches one endpoint', async () => {
        const page = await mountWithPeers();
        const group = page.shadowRoot!.querySelector<HTMLFieldSetElement>('fieldset.endpoint-group')!;
        const legend = group.querySelector<HTMLElement>('legend')!;
        expect(legend.textContent!.trim()).toBe('Endpoint');
        expect(legend.title).toContain('all match the same endpoint');
        const labels = Array.from(group.querySelectorAll('.multiselect-label, .field-label'))
            .map(el => el.textContent!.trim());
        expect(labels).toEqual(['Endpoint', 'Namespaces', 'Pods', 'Pod labels', 'IPs']);
    });
});

// The endpoint filters describe one endpoint, so the Pods menu only offers the Pods that can be
// that endpoint: on the end the Endpoint select picks, and in a selected namespace.
describe('AntreaFlowVisibilityPage — Pods filter', () => {
    async function mountWithPods(): Promise<AntreaFlowVisibilityPage> {
        const flows = [
            makeFlow({ srcNs: 'ns-a', srcPod: 'client', srcIP: '10.0.0.1', dstNs: 'ns-b', dstPod: 'frontend', dstIP: '10.0.0.2' }),
            makeFlow({ srcNs: 'ns-a', srcPod: 'client', srcIP: '10.0.0.1', dstNs: 'ns-c', dstPod: 'backend', dstIP: '10.0.0.3' }),
        ];
        const page = await mount(async () => sseResponse([flowEventChunk(flows)]), { search: '/?namespace=ns-a' });
        for (let i = 0; i < 5 && page.shadowRoot!.querySelectorAll('tbody tr').length < 2; i++) {
            await vi.advanceTimersByTimeAsync(1000);
            await page.updateComplete;
        }
        return page;
    }

    async function openMenu(page: AntreaFlowVisibilityPage, label: string): Promise<void> {
        const menu = Array.from(page.shadowRoot!.querySelectorAll('.multiselect'))
            .find(el => el.querySelector('.multiselect-label')!.textContent!.trim() === label)!;
        menu.querySelector<HTMLButtonElement>('.multiselect-btn')!.click();
        await page.updateComplete;
    }

    async function chooseEndpoint(page: AntreaFlowVisibilityPage, value: string): Promise<void> {
        const select = page.shadowRoot!.querySelector<HTMLSelectElement>('fieldset.endpoint-group select')!;
        select.value = value;
        select.dispatchEvent(new Event('change'));
        await page.updateComplete;
    }

    test('offers every Pod with no namespace selected', async () => {
        const page = await mountWithPods();
        await openMenu(page, 'Pods');
        expect(menuOptions(page)).toEqual(['backend', 'client', 'frontend']);
    });

    test('offers only the Pods of the selected namespaces', async () => {
        const page = await mountWithPods();
        await openMenu(page, 'Namespaces');
        toggleOption(page, 'ns-b');
        await page.updateComplete;
        await openMenu(page, 'Pods');
        expect(menuOptions(page)).toEqual(['frontend']);
    });

    test('offers only the Pods on the end the Endpoint select picks', async () => {
        const page = await mountWithPods();
        await chooseEndpoint(page, 'from');
        await openMenu(page, 'Pods');
        expect(menuOptions(page)).toEqual(['client']);

        await chooseEndpoint(page, 'to');
        expect(menuOptions(page)).toEqual(['backend', 'frontend']);
    });

    // The flows received are filtered by what is applied, so they cannot show that a selected Pod
    // does not fit: client is only seen in ns-a here, but ns-b may have a Pod of that name too.
    test('keeps a selected Pod that the selected namespaces do not show it in', async () => {
        const page = await mountWithPods();
        await openMenu(page, 'Pods');
        toggleOption(page, 'client');
        toggleOption(page, 'frontend');
        await page.updateComplete;

        await openMenu(page, 'Namespaces');
        toggleOption(page, 'ns-b');
        await page.updateComplete;
        expect((page as unknown as { _pendingPods: string[] })._pendingPods).toEqual(['client', 'frontend']);
        await openMenu(page, 'Pods');
        expect(menuOptions(page)).toEqual(['client', 'frontend']);
    });

    // Under Destination, the stream only has traffic into server, which says nothing about the
    // traffic it sends.
    test('keeps a selected Pod when the Endpoint changes, and sends it', async () => {
        const flows = [
            makeFlow({ srcNs: 'ns-a', srcPod: 'client', srcIP: '10.0.0.1', dstNs: 'ns-a', dstPod: 'server', dstIP: '10.0.0.2' }),
        ];
        const fetchMock = vi.fn(async () => sseResponse([flowEventChunk(flows)]));
        const page = await mount(fetchMock, { search: '/?namespace=ns-a' });
        for (let i = 0; i < 5 && page.shadowRoot!.querySelectorAll('tbody tr').length < 1; i++) {
            await vi.advanceTimersByTimeAsync(1000);
            await page.updateComplete;
        }
        const apply = async () => {
            page.shadowRoot!.querySelector<HTMLElement>('.filter-actions antrea-button')!.click();
            await page.updateComplete;
            await vi.advanceTimersByTimeAsync(0);
        };

        await chooseEndpoint(page, 'to');
        await openMenu(page, 'Pods');
        toggleOption(page, 'server');
        await page.updateComplete;
        await apply();

        await chooseEndpoint(page, 'from');
        await apply();
        const url = String(streamCalls(fetchMock).at(-1)![0]);
        expect(url).toContain('pods=server');
        expect(url).toContain('direction=from');
    });
});

describe('searchFilterOptions', () => {
    const options = ['ns-b/backend', 'ns-b/frontend', 'ns-c/frontend'];

    test('matches by prefix, ignoring case and surrounding spaces', () => {
        expect(searchFilterOptions(options, [], '  NS-B/F ').items).toEqual(['ns-b/frontend']);
    });

    test('matches a Service by its name alone', () => {
        expect(searchFilterOptions(options, [], 'front').items).toEqual(['ns-b/frontend', 'ns-c/frontend']);
    });

    test('does not match inside a name', () => {
        expect(searchFilterOptions(options, [], 'end').items).toEqual([]);
    });

    test('lists the selected options first, whatever is typed', () => {
        expect(searchFilterOptions(options, ['ns-c/frontend'], 'ns-b').items)
            .toEqual(['ns-c/frontend', 'ns-b/backend', 'ns-b/frontend']);
    });

    test('shows at most 10 unselected matches, in the order given, and says there are more', () => {
        const many = Array.from({ length: 12 }, (_, i) => `pod-${String(i).padStart(2, '0')}`);
        const { items, more } = searchFilterOptions(many, ['pod-11'], '');
        expect(items).toEqual(['pod-11', ...many.slice(0, 10)]);
        expect(more).toBe(true);
        expect(searchFilterOptions(many, [], 'pod-1').more).toBe(false);
    });
});

describe('typed names in the filter menus', () => {
    test('a Namespace is an RFC 1123 label', () => {
        expect(typedNamespace('ns-x')).toEqual({ value: 'ns-x' });
        expect(typedNamespace('NS-X').error).toBeDefined();
    });

    // As Kubernetes validates it: no limit on the parts between dots, unlike a namespace name.
    test('a Pod is a DNS subdomain of at most 253 characters', () => {
        expect(typedPod('redis-0')).toEqual({ value: 'redis-0' });
        expect(typedPod('web.v2')).toEqual({ value: 'web.v2' });
        expect(typedPod('a'.repeat(64))).toEqual({ value: 'a'.repeat(64) });
        expect(typedPod('a'.repeat(253))).toEqual({ value: 'a'.repeat(253) });
        expect(typedPod('a'.repeat(254)).error).toBeDefined();
        expect(typedPod('Redis').error).toBeDefined();
        expect(typedPod('web..v2').error).toBeDefined();
        expect(typedPod('-web').error).toBeDefined();
    });

    test('a Service is namespace/name', () => {
        expect(typedService('ns-b/frontend', '')).toEqual({ value: 'ns-b/frontend' });
        expect(typedService('ns-b/', 'ns-a').error).toBeDefined();
        expect(typedService('a/b/c', 'ns-a').error).toBeDefined();
    });

    // A bare name across every namespace rarely means anything to a user, so it is only taken
    // where the scope gives it a namespace, and the result shows that namespace.
    test('a bare Service name takes the observed namespace, and needs one', () => {
        expect(typedService('frontend', 'ns-a')).toEqual({ value: 'ns-a/frontend' });
        expect(typedService('frontend', '').error).toContain('namespace/name');
    });
});

describe('AntreaFlowVisibilityPage — filter menu search', () => {
    test('typing in an open menu narrows it, and reopening the menu clears the search', async () => {
        const flows = Array.from({ length: 12 }, (_, i) => makeFlow({
            srcNs: 'ns-a', srcPod: `client-${String(i).padStart(2, '0')}`, srcIP: `10.0.0.${i + 1}`,
            dstNs: 'ns-b', dstPod: 'server', dstIP: '10.0.1.1',
        }));
        const page = await mount(async () => sseResponse([flowEventChunk(flows)]), { search: '/?namespace=ns-a' });
        for (let i = 0; i < 5 && page.shadowRoot!.querySelectorAll('tbody tr').length < 12; i++) {
            await vi.advanceTimersByTimeAsync(1000);
            await page.updateComplete;
        }
        const menu = () => Array.from(page.shadowRoot!.querySelectorAll('.multiselect'))
            .find(el => el.querySelector('.multiselect-label')!.textContent!.trim() === 'Pods')!;
        const toggleMenu = async () => {
            menu().querySelector<HTMLButtonElement>('.multiselect-btn')!.click();
            await page.updateComplete;
        };

        await toggleMenu();
        const search = menu().querySelector<HTMLInputElement>('.multiselect-search')!;
        expect(page.shadowRoot!.activeElement).toBe(search);
        expect(menuOptions(page)).toHaveLength(10);
        expect(menu().querySelector('.multiselect-hint:last-child')!.textContent).toContain('type to search');

        search.value = 'serv';
        search.dispatchEvent(new Event('input'));
        await page.updateComplete;
        expect(menuOptions(page)).toEqual(['server']);

        await toggleMenu();
        await toggleMenu();
        expect(menu().querySelector<HTMLInputElement>('.multiselect-search')!.value).toBe('');
        expect(menuOptions(page)).toHaveLength(10);
    });

    async function mountForTyping(): Promise<AntreaFlowVisibilityPage> {
        const flows = [makeFlow({ srcNs: 'ns-a', srcPod: 'client', srcIP: '10.0.0.1', dstNs: 'ns-b', dstPod: 'server', dstIP: '10.0.1.1' })];
        const page = await mount(async () => sseResponse([flowEventChunk(flows)]), { search: '/?namespace=ns-a' });
        for (let i = 0; i < 5 && page.shadowRoot!.querySelectorAll('tbody tr').length < 1; i++) {
            await vi.advanceTimersByTimeAsync(1000);
            await page.updateComplete;
        }
        return page;
    }

    function menuOf(page: AntreaFlowVisibilityPage, label: string): Element {
        return Array.from(page.shadowRoot!.querySelectorAll('.multiselect'))
            .find(el => el.querySelector('.multiselect-label')!.textContent!.trim() === label)!;
    }

    async function typeIn(page: AntreaFlowVisibilityPage, label: string, text: string): Promise<HTMLInputElement> {
        menuOf(page, label).querySelector<HTMLButtonElement>('.multiselect-btn')!.click();
        await page.updateComplete;
        const search = menuOf(page, label).querySelector<HTMLInputElement>('.multiselect-search')!;
        search.value = text;
        search.dispatchEvent(new Event('input'));
        await page.updateComplete;
        return search;
    }

    // The Flow Aggregator applies the filter, so a name no flow shows yet still matches the
    // traffic that arrives later.
    test('a name no flow shows can be added from the search box', async () => {
        const page = await mountForTyping();
        await typeIn(page, 'Pods', 'redis-0');
        const add = menuOf(page, 'Pods').querySelector<HTMLButtonElement>('.multiselect-add')!;
        expect(add.textContent).toContain('Add "redis-0"');
        // As from the keyboard: focus on the button, which adding removes.
        add.focus();
        add.click();
        await page.updateComplete;
        await page.updateComplete;
        expect((page as unknown as { _pendingPods: string[] })._pendingPods).toEqual(['redis-0']);
        expect(menuOptions(page)).toContain('redis-0');
        // The button is gone, so the focus goes back to the search box rather than nowhere.
        expect(page.shadowRoot!.activeElement).toBe(menuOf(page, 'Pods').querySelector('.multiselect-search'));
    });

    test('Enter adds the typed name', async () => {
        const page = await mountForTyping();
        const search = await typeIn(page, 'Namespaces', 'ns-x');
        search.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }));
        await page.updateComplete;
        expect((page as unknown as { _pendingNs: string[] })._pendingNs).toEqual(['ns-x']);
    });

    test('a bare Service name is added in the observed namespace', async () => {
        const page = await mountForTyping();
        await typeIn(page, 'Services', 'frontend');
        expect(menuOf(page, 'Services').querySelector('.multiselect-add')!.textContent).toContain('Add "ns-a/frontend"');
    });

    test('an invalid name is not offered, and the menu says why', async () => {
        const page = await mountForTyping();
        await typeIn(page, 'Pods', 'Bad_Name');
        expect(menuOf(page, 'Pods').querySelector('.multiselect-add')).toBeNull();
        expect(menuOf(page, 'Pods').textContent).toContain('Not a valid Pod name');
    });
});

// Space on a focused checkbox ticks it, so the focus must stay on the option the user ticked: a
// second Space then unticks it rather than ticking whichever option took its place.
describe('AntreaFlowVisibilityPage — keyboard focus in the filter menus', () => {
    async function mountWith(flows: Flow[]): Promise<AntreaFlowVisibilityPage> {
        const page = await mount(async () => sseResponse([flowEventChunk(flows)]), { search: '/?namespace=ns-a' });
        for (let i = 0; i < 5 && page.shadowRoot!.querySelectorAll('tbody tr').length < flows.length; i++) {
            await vi.advanceTimersByTimeAsync(1000);
            await page.updateComplete;
        }
        return page;
    }

    // A client in ns-a reaching bravo, charlie and delta, each in its own namespace and behind its
    // own Service, so that every menu has several options.
    function threePeers(): Flow[] {
        return ['bravo', 'charlie', 'delta'].map((name, i) => makeFlow({
            srcNs: 'ns-a', srcPod: 'alpha', srcIP: '10.0.0.1',
            dstNs: `ns-${name[0]}`, dstPod: name, dstIP: `10.0.1.${i + 1}`, destService: `ns-${name[0]}/${name}:http`,
        }));
    }

    function menuOf(page: AntreaFlowVisibilityPage, label: string): Element {
        return Array.from(page.shadowRoot!.querySelectorAll('.multiselect'))
            .find(el => el.querySelector('.multiselect-label')!.textContent!.trim() === label)!;
    }

    async function toggleMenu(page: AntreaFlowVisibilityPage, label: string): Promise<void> {
        menuOf(page, label).querySelector<HTMLButtonElement>('.multiselect-btn')!.click();
        await page.updateComplete;
    }

    function checkbox(page: AntreaFlowVisibilityPage, option: string): HTMLInputElement {
        const row = Array.from(page.shadowRoot!.querySelectorAll('.multiselect-option'))
            .find(el => el.textContent!.trim() === option)!;
        return row.querySelector<HTMLInputElement>('input[type="checkbox"]')!;
    }

    // The option the focused checkbox stands for.
    function focusedOption(page: AntreaFlowVisibilityPage): string | undefined {
        return page.shadowRoot!.activeElement?.closest('.multiselect-option')?.textContent!.trim();
    }

    test.each(['Namespaces', 'Pods', 'Services'])('%s: ticking an option keeps it in place and focused', async label => {
        const page = await mountWith(threePeers());
        await toggleMenu(page, label);
        const before = menuOptions(page);
        const last = before[before.length - 1];
        const box = checkbox(page, last);
        box.focus();
        box.click();
        await page.updateComplete;
        expect(menuOptions(page)).toEqual(before);
        expect(page.shadowRoot!.activeElement).toBe(box);
        expect(focusedOption(page)).toBe(last);
        expect(menuOf(page, label).querySelector('.multiselect-btn')!.textContent!.trim()).toBe(last);

        box.click();
        await page.updateComplete;
        expect(menuOf(page, label).querySelector('.multiselect-btn')!.textContent!.trim()).toBe('All');
    });

    function twelveClients(): Flow[] {
        return Array.from({ length: 12 }, (_, i) => makeFlow({
            srcNs: 'ns-a', srcPod: `client-${String(i).padStart(2, '0')}`, srcIP: `10.0.0.${i + 1}`,
            dstNs: 'ns-b', dstPod: 'server', dstIP: '10.0.1.1',
        }));
    }

    // As the stream's onFlows does, then drops the names of the given Pods' flows, as the bound
    // on the names seen would.
    async function deliver(page: AntreaFlowVisibilityPage, flows: Flow[], dropPods: string[] = []): Promise<void> {
        const internals = page as unknown as { _store: FlowStore; _seenNames: SeenNames; _entries: unknown[] };
        internals._store.upsertBatch(flows);
        internals._seenNames.recordBatch(flows);
        if (dropPods.length > 0) {
            internals._seenNames.reset();
            internals._seenNames.recordBatch(internals._store.getAll().map(e => e.flow).filter(f =>
                !dropPods.includes(f.k8s.sourcePodName) && !dropPods.includes(f.k8s.destinationPodName)));
        }
        internals._entries = internals._store.getAll();
        await page.updateComplete;
    }

    test('unticking a selected option keeps it in the menu, past the options shown', async () => {
        const page = await mountWith(twelveClients());
        await toggleMenu(page, 'Pods');
        const search = menuOf(page, 'Pods').querySelector<HTMLInputElement>('.multiselect-search')!;
        search.value = 'client-11';
        search.dispatchEvent(new Event('input'));
        await page.updateComplete;
        toggleOption(page, 'client-11');
        await toggleMenu(page, 'Pods');
        await toggleMenu(page, 'Pods');
        const before = menuOptions(page);
        expect(before[0]).toBe('client-11');

        const box = checkbox(page, 'client-11');
        box.focus();
        box.click();
        await page.updateComplete;
        expect(menuOptions(page)).toEqual(before);
        expect(focusedOption(page)).toBe('client-11');
    });

    test('unticking a typed name keeps it in the menu', async () => {
        const page = await mountWith(threePeers());
        await toggleMenu(page, 'Pods');
        const search = menuOf(page, 'Pods').querySelector<HTMLInputElement>('.multiselect-search')!;
        search.value = 'redis-0';
        search.dispatchEvent(new Event('input'));
        await page.updateComplete;
        search.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }));
        await page.updateComplete;
        const before = menuOptions(page);
        expect(before[0]).toBe('redis-0');

        const box = checkbox(page, 'redis-0');
        box.focus();
        box.click();
        await page.updateComplete;
        expect(menuOptions(page)).toEqual(before);
        expect(focusedOption(page)).toBe('redis-0');
    });

    test('unticking an option no longer offered keeps it in the menu', async () => {
        const page = await mountWith(threePeers());
        await toggleMenu(page, 'Pods');
        const before = menuOptions(page);
        const box = checkbox(page, 'delta');
        box.focus();
        box.click();
        await page.updateComplete;
        await deliver(page, [], ['delta']);

        box.click();
        await page.updateComplete;
        expect(menuOf(page, 'Pods').querySelector('.multiselect-btn')!.textContent!.trim()).toBe('All');
        expect(menuOptions(page)).toEqual(before);
        expect(focusedOption(page)).toBe('delta');
    });

    // Rows changing under the focus would move it to another option, or drop it with a row that
    // leaves.
    test('names arriving or dropped do not change the open menu, and show once it is reopened', async () => {
        const page = await mountWith(twelveClients());
        await toggleMenu(page, 'Pods');
        const before = menuOptions(page);
        expect(before.at(-1)).toBe('client-09');
        const box = checkbox(page, 'client-09');
        box.focus();

        await deliver(page, [makeFlow({ srcNs: 'ns-a', srcPod: 'aaron', srcIP: '10.0.0.99', dstNs: 'ns-b', dstPod: 'server', dstIP: '10.0.1.1' })]);
        expect(menuOptions(page)).toEqual(before);
        expect(focusedOption(page)).toBe('client-09');

        await deliver(page, [], ['client-09']);
        expect(menuOptions(page)).toEqual(before);
        expect(page.shadowRoot!.activeElement).toBe(box);
        expect(focusedOption(page)).toBe('client-09');

        await toggleMenu(page, 'Pods');
        await toggleMenu(page, 'Pods');
        expect(menuOptions(page)[0]).toBe('aaron');
        expect(menuOptions(page)).not.toContain('client-09');
    });

    // The offer follows the rows: a name that no row shows can be added, and one that a row shows
    // is not offered twice.
    test('the offer to add a typed name does not change as names arrive or are dropped', async () => {
        const page = await mountWith(threePeers());
        await toggleMenu(page, 'Pods');
        const search = menuOf(page, 'Pods').querySelector<HTMLInputElement>('.multiselect-search')!;
        search.value = 'redis-0';
        search.dispatchEvent(new Event('input'));
        await page.updateComplete;
        await deliver(page, [makeFlow({ srcNs: 'ns-a', srcPod: 'redis-0', srcIP: '10.0.0.98', dstNs: 'ns-b', dstPod: 'bravo', dstIP: '10.0.1.1' })]);
        expect(menuOf(page, 'Pods').querySelector('.multiselect-add')!.textContent).toContain('Add "redis-0"');

        search.value = 'delta';
        search.dispatchEvent(new Event('input'));
        await page.updateComplete;
        await deliver(page, [], ['delta']);
        expect(menuOptions(page)).toEqual(['delta']);
        expect(menuOf(page, 'Pods').querySelector('.multiselect-add')).toBeNull();
    });

    test('a scope change closes the open menu, whose options were of the old scope', async () => {
        const page = await mount(async () => sseResponse([flowEventChunk(threePeers())]), {
            search: '/?namespace=ns-a',
            flowAccess: () => flowAccessFixture({ namespaces: ['ns-a', 'ns-b'] }),
        });
        await vi.advanceTimersByTimeAsync(1000);
        await page.updateComplete;
        await toggleMenu(page, 'Pods');
        expect(menuOf(page, 'Pods').querySelector('.multiselect-dropdown')).not.toBeNull();
        await chooseScope(page, 'ns-b');
        expect(menuOf(page, 'Pods').querySelector('.multiselect-dropdown')).toBeNull();
    });

    // A Service is only ever a flow's destination, so Source empties the Services menu even while
    // it is open.
    test('an open Services menu has no options once the Endpoint is the source', async () => {
        const page = await mountWith(threePeers());
        await toggleMenu(page, 'Services');
        expect(menuOptions(page)).not.toEqual([]);
        const select = page.shadowRoot!.querySelector<HTMLSelectElement>('fieldset.endpoint-group select')!;
        select.value = 'from';
        select.dispatchEvent(new Event('change'));
        await page.updateComplete;
        expect(menuOf(page, 'Services').querySelector('.multiselect-dropdown')).not.toBeNull();
        expect(menuOptions(page)).toEqual([]);
    });
});

// The Flow Aggregator rejects a Service filter with the endpoint filters on the source (a Service
// is only ever a flow's destination), so the page never lets the two be applied together.
describe('AntreaFlowVisibilityPage — Services filter', () => {
    async function mountWithServices(): Promise<{ page: AntreaFlowVisibilityPage; fetchMock: ReturnType<typeof vi.fn> }> {
        const fetchMock = vi.fn(async () => sseResponse([flowEventChunk([
            makeFlow({ srcIP: '10.0.0.1', destService: 'ns-b/frontend:http' }),
            makeFlow({ srcIP: '10.0.0.2', destService: 'ns-c/frontend:http' }),
        ])]));
        const page = await mount(fetchMock, { search: '/?namespace=ns-a' });
        for (let i = 0; i < 5 && page.shadowRoot!.querySelectorAll('tbody tr').length < 2; i++) {
            await vi.advanceTimersByTimeAsync(1000);
            await page.updateComplete;
        }
        return { page, fetchMock };
    }

    async function openServiceMenu(page: AntreaFlowVisibilityPage): Promise<void> {
        const menu = Array.from(page.shadowRoot!.querySelectorAll('.multiselect'))
            .find(el => el.querySelector('.multiselect-label')!.textContent!.trim() === 'Services')!;
        menu.querySelector<HTMLButtonElement>('.multiselect-btn')!.click();
        await page.updateComplete;
    }

    function endpointSelect(page: AntreaFlowVisibilityPage): HTMLSelectElement {
        return page.shadowRoot!.querySelector<HTMLSelectElement>('fieldset.endpoint-group select')!;
    }

    // The Flow Aggregator matches a namespace/name value against that Service only: the same name
    // in two Namespaces is two Services, and selecting one must not ask for the other.
    test('applying a Service sends it with its namespace', async () => {
        const { page, fetchMock } = await mountWithServices();
        await openServiceMenu(page);
        expect(menuOptions(page)).toEqual(['ns-b/frontend', 'ns-c/frontend']);
        toggleOption(page, 'ns-b/frontend');
        await page.updateComplete;

        page.shadowRoot!.querySelector<HTMLElement>('.filter-actions antrea-button')!.click();
        await page.updateComplete;
        await vi.advanceTimersByTimeAsync(0);

        expect(streamCalls(fetchMock)).toHaveLength(2);
        const url = new URL(streamCalls(fetchMock)[1][0] as string, 'http://localhost');
        expect(url.searchParams.get('services')).toBe('ns-b/frontend');
    });

    test('the endpoint cannot be the source while a Service is selected', async () => {
        const { page } = await mountWithServices();
        const from = () => endpointSelect(page).querySelector<HTMLOptionElement>('option[value="from"]')!;
        expect(from().disabled).toBe(false);

        await openServiceMenu(page);
        toggleOption(page, 'ns-b/frontend');
        await page.updateComplete;
        expect(from().disabled).toBe(true);
        expect(from().title).toContain('Source is not available while a Service is selected');
        expect(endpointSelect(page).title).toContain('Source is not available while a Service is selected');

        toggleOption(page, 'ns-b/frontend');
        await page.updateComplete;
        expect(from().disabled).toBe(false);
    });

    test('with the endpoint on the source, the Services menu offers no Service and says why', async () => {
        const { page } = await mountWithServices();
        const select = endpointSelect(page);
        select.value = 'from';
        select.dispatchEvent(new Event('change'));
        await page.updateComplete;

        await openServiceMenu(page);
        expect(menuOptions(page)).toEqual([]);
        expect(page.shadowRoot!.querySelector('.multiselect-hint')!.textContent)
            .toContain('not available while the endpoint is the source');
        // Nor can one be typed: the Flow Aggregator rejects a Service with the source.
        expect(page.shadowRoot!.querySelector('.multiselect-search')).toBeNull();
    });
});

// The observed namespace is a valid filter value (with From or To it selects the traffic leaving or
// entering it), so moving the scope onto a namespace the filter names leaves the filter alone.
describe('AntreaFlowVisibilityPage — a scope change and the applied namespaces', () => {
    test('keeps the applied namespaces when the scope moves onto one of them', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        const page = await mount(fetchMock, {
            search: '/?namespace=default',
            flowAccess: () => flowAccessFixture({
                namespaces: ['default', 'ns-x'],
            }),
        });
        (page as unknown as { _applyFilter(filter: FlowStreamFilter): void })
            ._applyFilter({ namespaces: ['ns-x', 'ns-y'] });
        await page.updateComplete;
        await vi.advanceTimersByTimeAsync(0);

        await chooseScope(page, 'ns-x');
        await vi.advanceTimersByTimeAsync(0);

        const calls = streamCalls(fetchMock);
        const params = new URL(calls[calls.length - 1][0] as string, 'http://example.test').searchParams;
        expect(params.get('observedNamespace')).toBe('ns-x');
        expect(params.get('namespaces')).toBe('ns-x,ns-y');
    });
});

// The filter menus offer the names seen since the scope was selected, and not only those of the
// flows on the page, which the applied filters decide: a filter would otherwise take away the
// names the next filter needs.
describe('AntreaFlowVisibilityPage — the names the filter menus offer', () => {
    // In ns-a: client -> server, server -> the Service ns-b/web, and web -> redis-0 in ns-b.
    const unfiltered = [
        makeFlow({ srcNs: 'ns-a', srcPod: 'client', srcIP: '10.0.0.1', dstNs: 'ns-a', dstPod: 'server', dstIP: '10.0.0.2' }),
        makeFlow({ srcNs: 'ns-a', srcPod: 'server', srcIP: '10.0.0.2', dstNs: 'ns-b', dstPod: 'web', dstIP: '10.0.1.1', destService: 'ns-b/web:http' }),
        makeFlow({ srcNs: 'ns-b', srcPod: 'web', srcIP: '10.0.1.1', dstNs: 'ns-b', dstPod: 'redis-0', dstIP: '10.0.1.2' }),
    ];
    const allPods = ['client', 'redis-0', 'server', 'web'];
    const inNsB = (pod: string) => makeFlow({ srcNs: 'ns-b', srcPod: pod, srcIP: '10.0.2.1', dstNs: 'ns-b', dstPod: 'db', dstIP: '10.0.2.2' });

    function scopeOf(url: string): string | null {
        return new URL(url, 'http://test').searchParams.get('observedNamespace');
    }

    function filtered(url: string): boolean {
        return new URL(url, 'http://test').searchParams.has('pods');
    }

    /** Opens the menu, reads its options and closes it again. */
    async function optionsOf(page: AntreaFlowVisibilityPage, label: string): Promise<string[]> {
        const menu = Array.from(page.shadowRoot!.querySelectorAll('.multiselect'))
            .find(el => el.querySelector('.multiselect-label')!.textContent!.trim() === label)!;
        const button = menu.querySelector<HTMLButtonElement>('.multiselect-btn')!;
        button.click();
        await page.updateComplete;
        const options = menuOptions(page);
        button.click();
        await page.updateComplete;
        return options;
    }

    async function hintsOf(page: AntreaFlowVisibilityPage, label: string): Promise<string[]> {
        const menu = Array.from(page.shadowRoot!.querySelectorAll('.multiselect'))
            .find(el => el.querySelector('.multiselect-label')!.textContent!.trim() === label)!;
        const button = menu.querySelector<HTMLButtonElement>('.multiselect-btn')!;
        button.click();
        await page.updateComplete;
        const hints = Array.from(menu.querySelectorAll('.multiselect-hint')).map(el => el.textContent!.trim());
        button.click();
        await page.updateComplete;
        return hints;
    }

    /** Lets the stream's records reach the page, past the client's batch interval. */
    async function settle(page: AntreaFlowVisibilityPage): Promise<void> {
        await vi.advanceTimersByTimeAsync(2000);
        await page.updateComplete;
    }

    async function apply(page: AntreaFlowVisibilityPage, filter: FlowStreamFilter): Promise<void> {
        (page as unknown as { _applyFilter(filter: FlowStreamFilter): void })._applyFilter(filter);
        await page.updateComplete;
        await settle(page);
    }

    async function click(page: AntreaFlowVisibilityPage, text: string): Promise<void> {
        Array.from(page.shadowRoot!.querySelectorAll<HTMLElement>('.filter-actions antrea-button'))
            .find(b => b.textContent!.trim() === text)!.click();
        await page.updateComplete;
        await settle(page);
    }

    function rows(page: AntreaFlowVisibilityPage): number {
        return page.shadowRoot!.querySelectorAll('tbody tr').length;
    }

    // ns-a answers with every flow unfiltered and with client -> server under the Pod filter;
    // ns-b with `nsB` under the Pod filter and with `nsBUnfiltered` otherwise.
    async function mountScopes(opts: {
        nsB?: Flow[], nsBUnfiltered?: Flow[], nsBStatus?: number, search?: string,
    } = {}): Promise<{ page: AntreaFlowVisibilityPage, fetchMock: ReturnType<typeof vi.fn> }> {
        const fetchMock = vi.fn(async (url: string) => {
            if (scopeOf(url) === 'ns-b') {
                return sseResponse([flowEventChunk(filtered(url) ? opts.nsB ?? [] : opts.nsBUnfiltered ?? [])], opts.nsBStatus);
            }
            return sseResponse([flowEventChunk(filtered(url) ? [unfiltered[0]] : unfiltered)]);
        });
        const page = await mount(fetchMock, {
            search: opts.search ?? '/?namespace=ns-a',
            flowAccess: () => flowAccessFixture({ namespaces: ['ns-a', 'ns-b'] }),
        });
        await settle(page);
        return { page, fetchMock };
    }

    test('a filter applied keeps offering the names seen before it', async () => {
        const { page } = await mountScopes();
        await apply(page, { pods: ['server'] });
        expect(rows(page)).toBe(1);

        expect(await optionsOf(page, 'Namespaces')).toEqual(['ns-a', 'ns-b']);
        expect(await optionsOf(page, 'Pods')).toEqual(allPods);
        expect(await optionsOf(page, 'Services')).toEqual(['ns-b/web']);
    });

    test('Reset and Clear keep them', async () => {
        let calls = 0;
        const fetchMock = vi.fn(async () => sseResponse([flowEventChunk(calls++ === 0 ? unfiltered : [])]));
        const page = await mount(fetchMock, { search: '/?namespace=ns-a' });
        await settle(page);
        await apply(page, { pods: ['server'] });

        await click(page, 'Reset');
        expect(streamCalls(fetchMock)).toHaveLength(3);
        expect(await optionsOf(page, 'Pods')).toEqual(allPods);

        await click(page, 'Clear');
        expect(await optionsOf(page, 'Pods')).toEqual(allPods);
    });

    // The store empties, as for any new filter, and no stream opens to fill it.
    test('a filter applied while paused keeps them', async () => {
        const { page } = await mountScopes();
        await click(page, 'Pause');
        await apply(page, { pods: ['server'] });
        expect(rows(page)).toBe(0);
        expect(await optionsOf(page, 'Pods')).toEqual(allPods);
    });

    test('another scope starts with its own names', async () => {
        const { page } = await mountScopes({ nsBUnfiltered: [inNsB('other')] });
        await chooseScope(page, 'ns-b');
        await settle(page);

        expect(await optionsOf(page, 'Namespaces')).toEqual(['ns-b']);
        expect(await optionsOf(page, 'Pods')).toEqual(['db', 'other']);
        expect(await optionsOf(page, 'Services')).toEqual([]);
    });

    // Back and Forward.
    test('another scope taken from the URL starts with its own names', async () => {
        const { page } = await mountScopes({ nsBUnfiltered: [inNsB('other')] });
        page.locationSearch = '?namespace=ns-a';
        await page.updateComplete;

        page.locationSearch = '?namespace=ns-b';
        await page.updateComplete;
        await settle(page);

        expect(await optionsOf(page, 'Pods')).toEqual(['db', 'other']);
    });

    // Nothing arrives to replace the old scope's names, which must go all the same.
    test.each(['refused', 'chosen while paused'])('another scope that is %s offers none', async how => {
        const { page } = await mountScopes({ nsBUnfiltered: [inNsB('other')], nsBStatus: how === 'refused' ? 403 : 200 });
        if (how === 'chosen while paused') await click(page, 'Pause');
        await chooseScope(page, 'ns-b');
        await settle(page);

        expect(await optionsOf(page, 'Namespaces')).toEqual([]);
        expect(await optionsOf(page, 'Pods')).toEqual([]);
        expect(await hintsOf(page, 'Pods')).toContain('None seen in this scope yet.');
    });

    // Stopping the old scope's client flushes its pending batch, which is recorded before the
    // names are reset.
    test('records still pending for the old scope are not offered under the new one', async () => {
        const fetchMock = vi.fn(async (url: string) => scopeOf(url) === 'ns-a'
            ? sseResponse([flowEventChunk([makeFlow({ srcNs: 'ns-a', srcPod: 'old-pod' })])])
            : sseResponse([]));
        const page = await mount(fetchMock, {
            search: '/?namespace=ns-a',
            flowAccess: () => flowAccessFixture({ namespaces: ['ns-a', 'ns-b'] }),
        });
        // Received, and still inside the batch interval.
        await vi.advanceTimersByTimeAsync(10);

        await chooseScope(page, 'ns-b');
        await settle(page);

        expect(await optionsOf(page, 'Pods')).toEqual([]);
    });

    // The applied filters stay when the scope changes, so the new scope's names come from the
    // filtered flows only, which the empty menu says, until Reset.
    test('another scope chosen with filters applied fills from the filtered flows', async () => {
        const { page } = await mountScopes({ nsB: [], nsBUnfiltered: [inNsB('other')] });
        await apply(page, { pods: ['server'] });
        await chooseScope(page, 'ns-b');
        await settle(page);

        expect(await optionsOf(page, 'Pods')).toEqual([]);
        expect(await hintsOf(page, 'Pods')).toContain(
            'None seen in this scope yet. The applied filters limit the flows received: Reset may show more.');

        await click(page, 'Reset');
        expect(await optionsOf(page, 'Pods')).toEqual(['db', 'other']);
    });

    // A switch to a query over a time window asks the same scope again, here of another flow source,
    // as a host may name one that keeps history. With no scope in the URL, the page holds none
    // until the new mode's default is known, which is the same namespace here.
    test.each([['in the URL', '/?namespace=ns-a'], ['by default', '/']])(
        'a change of mode keeps them, with the scope %s', async (_, search) => {
            let live = 0;
            const fetchMock = vi.fn(async (url: string) => new URL(url, 'http://test').searchParams.get('follow') === 'false'
                ? finiteResponse([FINITE_COMPLETE])
                : sseResponse([flowEventChunk(live++ === 0 ? unfiltered : [])]));
            const page = await mount(fetchMock, { search, flowAccess: () => flowAccessFixture({ namespaces: ['ns-a'] }) });
            await settle(page);

            await setQuery(page, { follow: false, source: 'flow-history' });
            await settle(page);
            expect(await optionsOf(page, 'Pods')).toEqual(allPods);

            await setQuery(page, undefined);
            await settle(page);
            expect(await optionsOf(page, 'Pods')).toEqual(allPods);
            expect(streamCalls(fetchMock).map(c => scopeOf(c[0] as string))).toEqual(['ns-a', 'ns-a', 'ns-a']);
        },
    );

    // The Pods menu narrows the names seen as it narrows the flows on the page: by the end the
    // pending direction picks, and by the pending namespaces. Under the applied filter, only
    // client -> server is on the page.
    test('the Pods seen are narrowed by the pending direction and namespaces', async () => {
        const { page } = await mountScopes();
        await apply(page, { pods: ['server'] });
        const priv = page as unknown as { _pendingDirection: string; _pendingNs: string[] };

        priv._pendingDirection = 'from';
        await page.updateComplete;
        expect(await optionsOf(page, 'Pods')).toEqual(['client', 'server', 'web']);

        priv._pendingDirection = 'to';
        await page.updateComplete;
        expect(await optionsOf(page, 'Pods')).toEqual(['redis-0', 'server', 'web']);

        priv._pendingDirection = 'both';
        priv._pendingNs = ['ns-b'];
        await page.updateComplete;
        expect(await optionsOf(page, 'Pods')).toEqual(['redis-0', 'web']);
    });

    // An invalid scope in the URL selects none: the names kept are the last scope's, which the
    // menus do not offer for none.
    test('no scope offers none', async () => {
        const { page } = await mountScopes();
        page.locationSearch = '?namespace=ns-a';
        await page.updateComplete;

        page.locationSearch = '?namespace=Not_Valid';
        await page.updateComplete;
        await settle(page);

        expect(await optionsOf(page, 'Namespaces')).toEqual([]);
        expect(await optionsOf(page, 'Pods')).toEqual([]);
        expect(await optionsOf(page, 'Services')).toEqual([]);
        expect(await hintsOf(page, 'Pods')).not.toContain('None seen in this scope yet.');
    });

    // Pods were seen, but none on the end or in the namespace selected: the menu is empty, and saying
    // that none was seen would be wrong.
    test('a Pods menu narrowed to nothing does not say none was seen', async () => {
        const { page } = await mountScopes();
        await apply(page, { pods: ['server'] });
        (page as unknown as { _pendingNs: string[] })._pendingNs = ['ns-c'];
        await page.updateComplete;

        expect(await optionsOf(page, 'Pods')).toEqual([]);
        expect((await hintsOf(page, 'Pods')).filter(h => h.startsWith('None seen'))).toEqual([]);
    });

    // The scope has not changed, though the stream had to be opened again.
    test('an element moved in the document keeps them', async () => {
        let calls = 0;
        const fetchMock = vi.fn(async () => sseResponse([flowEventChunk(calls++ === 0 ? unfiltered : [])]));
        const page = await mount(fetchMock, { search: '/?namespace=ns-a' });
        await settle(page);

        page.remove();
        document.body.appendChild(page);
        await page.updateComplete;
        await settle(page);

        expect(streamCalls(fetchMock)).toHaveLength(2);
        expect(rows(page)).toBe(0);
        expect(await optionsOf(page, 'Pods')).toEqual(allPods);
    });
});

describe('AntreaFlowVisibilityPage — teardown', () => {
    test('disconnectedCallback stops the stream client and removes the pointerdown listener', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        const page = await mount(fetchMock);
        await page.updateComplete;
        await vi.advanceTimersByTimeAsync(0);

        const removeSpy = vi.spyOn(window, 'removeEventListener');
        page.remove();
        el = undefined;

        expect(removeSpy).toHaveBeenCalledWith('pointerdown', expect.any(Function));
        // No further fetches should happen once torn down.
        const callsBeforeWait = fetchMock.mock.calls.length;
        await vi.advanceTimersByTimeAsync(60_000);
        expect(fetchMock.mock.calls.length).toBe(callsBeforeWait);
    });
});

// Redaction reaches the list through the per-endpoint disclosure markers the Flow Aggregator puts
// on each record. antrea-ui redacts nothing of its own; all of this is rendering.
describe('AntreaFlowVisibilityPage — disclosure in the flow list', () => {
    async function rowFor(flow: Flow): Promise<HTMLTableRowElement> {
        const page = await mount(async () => sseResponse([flowEventChunk([flow])]));
        for (let i = 0; i < 5 && page.shadowRoot!.querySelectorAll('tbody tr').length < 1; i++) {
            await vi.advanceTimersByTimeAsync(1000);
            await page.updateComplete;
        }
        return page.shadowRoot!.querySelector<HTMLTableRowElement>('tbody tr')!;
    }

    function cell(row: HTMLTableRowElement, index: number): HTMLTableCellElement {
        return row.querySelectorAll('td')[index];
    }

    // A cluster-scoped policy always has a name, so an empty one is withheld even on an endpoint
    // disclosed in full, where a Namespaced policy's empty name is not.
    test('an empty cluster-scoped policy name is marked as hidden on a fully disclosed endpoint', async () => {
        const cluster = await rowFor(makeFlow({
            ingressPolicy: '', ingressPolicyType: NetworkPolicyType.ACNP, ingressAction: NetworkPolicyRuleAction.Drop,
        }));
        expect(cluster.textContent).toContain('Dropped (policy hidden)');
    });

    test('an empty Namespaced policy name is not marked as hidden on a fully disclosed endpoint', async () => {
        const namespaced = await rowFor(makeFlow({
            ingressPolicy: '', ingressPolicyType: NetworkPolicyType.ANP, ingressAction: NetworkPolicyRuleAction.Drop,
        }));
        expect(namespaced.textContent).toContain('Dropped');
        expect(namespaced.textContent).not.toContain('policy hidden');
    });

    // Identity is deliberately indistinguishable from Full: the whole difference between them is
    // Node placement and the Antrea Egress, and this UI renders neither.
    test('an Identity-tier peer is not marked as redacted', async () => {
        const row = await rowFor(makeFlow({ dstDisclosure: EndpointDisclosure.Identity, dstNs: 'ns-b', dstPod: 'db-abc12' }));
        expect(cell(row, 2).textContent).toContain('ns-b/db-abc12');
        expect(cell(row, 2).querySelector('.redacted')).toBeNull();
    });

    test('a Flow-tier peer on an allowed connection keeps its namespace, marked and explained', async () => {
        const row = await rowFor(makeFlow({
            dstDisclosure: EndpointDisclosure.Flow, dstNs: 'ns-c', dstPod: '', dstIP: '10.0.0.7',
        }));
        const dst = cell(row, 2);
        expect(dst.textContent).toContain('ns-c/\u27e8hidden\u27e9');
        expect(dst.querySelector('.redacted')).not.toBeNull();
        expect(dst.querySelector('.redacted')!.getAttribute('title')).toContain('watch on flows or get on flows/identity in ns-c');
    });

    // The destination of a denied connection: its namespace is cleared too, so only the address is
    // left — and there is no namespace to name, so no tooltip either.
    test('a Flow-tier destination on a denied connection shows the address alone, with no tooltip', async () => {
        const row = await rowFor(makeFlow({
            dstDisclosure: EndpointDisclosure.Flow, dstNs: '', dstPod: '', dstIP: '10.0.0.9',
            ingressPolicy: '', ingressAction: NetworkPolicyRuleAction.Drop,
        }));
        const dst = cell(row, 2);
        expect(dst.textContent).toContain('10.0.0.9');
        expect(dst.querySelector('.redacted')).not.toBeNull();
        expect(dst.querySelector('.redacted')!.getAttribute('title')).not.toContain('flows/identity');
    });

    // Collapsing only the unidentified side: the source is in scope and stays whole.
    test('the identified end of a redacted flow is untouched', async () => {
        const row = await rowFor(makeFlow({ dstDisclosure: EndpointDisclosure.Flow, dstNs: 'ns-c', dstPod: '' }));
        expect(cell(row, 1).textContent).toContain('default/client-abc12');
        expect(cell(row, 1).querySelector('.redacted')).toBeNull();
    });

    test('a withheld Dest Service is marked with its own explanation', async () => {
        const row = await rowFor(makeFlow({
            dstDisclosure: EndpointDisclosure.Flow, svcDisclosure: EndpointDisclosure.Flow, dstNs: 'ns-c', dstPod: '',
        }));
        const svc = cell(row, 3);
        expect(svc.querySelector('.redacted')).not.toBeNull();
        expect(svc.querySelector('.redacted')!.getAttribute('title')).toContain('flows/identity in its namespace');
    });

    // The Service is disclosed at the tier of its own Namespace, which need not be the destination
    // endpoint's: each cell follows its own marker.
    test('a disclosed Dest Service shows on a withheld destination', async () => {
        const row = await rowFor(makeFlow({
            dstDisclosure: EndpointDisclosure.Flow, dstNs: 'ns-c', dstPod: '', destService: 'ns-b/frontend:http',
        }));
        expect(cell(row, 2).querySelector('.redacted')).not.toBeNull();
        expect(cell(row, 3).textContent).toContain('ns-b/frontend');
        expect(cell(row, 3).querySelector('.redacted')).toBeNull();
    });

    // A withheld destination says nothing about whether there was a Service: with the Service
    // marker at Full, the record had none, and the cell must not claim one was withheld.
    test('a withheld destination with no Service shows an unmarked dash', async () => {
        const row = await rowFor(makeFlow({ dstDisclosure: EndpointDisclosure.Flow, dstNs: 'ns-c', dstPod: '' }));
        expect(cell(row, 2).querySelector('.redacted')).not.toBeNull();
        expect(cell(row, 3).textContent!.trim()).toBe('-');
        expect(cell(row, 3).querySelector('.redacted')).toBeNull();
    });

    // Nothing of an external destination is withheld, but its Service has a Namespace of its own,
    // and the Service marker tells a withheld one apart from none.
    test('a withheld Dest Service is marked on an external destination', async () => {
        const row = await rowFor(makeFlow({
            flowType: FlowType.ToExternal, dstDisclosure: EndpointDisclosure.Flow, dstNs: '', dstPod: '',
            dstIP: '203.0.113.10', svcDisclosure: EndpointDisclosure.Flow,
        }));
        expect(cell(row, 2).querySelector('.redacted')).toBeNull();
        expect(cell(row, 3).querySelector('.redacted')).not.toBeNull();
    });

    test('a withheld Dest Service is marked on an identified destination', async () => {
        const row = await rowFor(makeFlow({ dstNs: 'ns-a', dstPod: 'db-abc12', svcDisclosure: EndpointDisclosure.Flow }));
        expect(cell(row, 2).querySelector('.redacted')).toBeNull();
        expect(cell(row, 3).querySelector('.redacted')).not.toBeNull();
    });

    // The most diagnostically useful thing redaction preserves: the rule action outlives the
    // policy name, so a drop to a peer you cannot see still reads as a drop.
    test('a dropped flow with a withheld policy name renders the action, not a bare dash', async () => {
        const row = await rowFor(makeFlow({
            dstDisclosure: EndpointDisclosure.Flow, dstNs: 'ns-c', dstPod: '',
            ingressPolicy: '', ingressAction: NetworkPolicyRuleAction.Drop,
        }));
        expect(cell(row, 8).textContent).toContain('Dropped (policy hidden)');
        expect(cell(row, 8).querySelector('.redacted')).toBeNull();
    });

    test('a flow no policy matched still renders a dash', async () => {
        const row = await rowFor(makeFlow());
        expect(cell(row, 8).textContent!.trim()).toBe('-');
    });
});

// The service map's two collapsed forms. antrea-ui redacts nothing itself; these are renderings
// of the disclosure markers the Flow Aggregator put on each record.
describe('AntreaFlowVisibilityPage — collapsed peers in the service map', () => {
    interface TestNode { id: string; shortName: string; namespace: string; kind: string; detail: string }
    interface TestEdge { source: string; target: string; connectionCount: number; totalBytesForward: number }
    interface TestGraph { nodes: TestNode[]; edges: TestEdge[] }

    async function graphFor(flows: Flow[]): Promise<{ page: AntreaFlowVisibilityPage; graph: TestGraph }> {
        const page = await mount(async () => sseResponse([flowEventChunk(flows)]));
        for (let i = 0; i < 5 && page.shadowRoot!.querySelectorAll('tbody tr').length < flows.length; i++) {
            await vi.advanceTimersByTimeAsync(1000);
            await page.updateComplete;
        }
        page.viewMode = 'map';
        await page.updateComplete;
        return { page, graph: (page as unknown as { _graphRef: TestGraph })._graphRef };
    }

    function byKind(graph: TestGraph, kind: string): TestNode[] {
        return graph.nodes.filter(n => n.kind === kind);
    }

    // Form A: the namespace survived, its contents did not. An empty namespace hull, labelled
    // with the count of distinct peer addresses — not of workloads, which is unknowable.
    test('a named but unidentified peer collapses into one node per namespace', async () => {
        const { graph } = await graphFor([
            makeFlow({ srcIP: '10.0.0.1', dstIP: '10.1.0.1', dstNs: 'ns-c', dstPod: '', dstDisclosure: EndpointDisclosure.Flow }),
            makeFlow({ srcIP: '10.0.0.1', dstIP: '10.1.0.2', dstNs: 'ns-c', dstPod: '', dstDisclosure: EndpointDisclosure.Flow }),
        ]);

        const collapsed = byKind(graph, 'undisclosedNamespace');
        expect(collapsed).toHaveLength(1);
        expect(collapsed[0].shortName).toBe('ns-c');
        expect(collapsed[0].detail).toBe('2 addresses');
    });

    // Collapse only the unidentified side: which of the caller's own workloads is talking
    // outward is the one thing they are entitled to see.
    test('the source of a flow to a collapsed peer stays at workload granularity', async () => {
        const { graph } = await graphFor([
            makeFlow({ dstNs: 'ns-c', dstPod: '', dstDisclosure: EndpointDisclosure.Flow }),
        ]);

        const workloads = byKind(graph, 'workload');
        expect(workloads).toHaveLength(1);
        expect(workloads[0].shortName).toBe('client');
        expect(workloads[0].id).toBe('default/client');
    });

    // Form B: a denied connection costs its destination the namespace too, so there is nothing left to
    // group by but the identified counterpart — one leaf per source workload, never one shared
    // node, which would assert that every denied flow went to the same place.
    test('a fully anonymous peer collapses into one leaf per source workload', async () => {
        const denied = { dstNs: '', dstPod: '', dstDisclosure: EndpointDisclosure.Flow, ingressAction: NetworkPolicyRuleAction.Drop };
        const { graph } = await graphFor([
            makeFlow({ ...denied, srcPod: 'aaa-abc12', srcIP: '10.0.0.1', dstIP: '10.1.0.1' }),
            makeFlow({ ...denied, srcPod: 'aaa-abc12', srcIP: '10.0.0.1', dstIP: '10.1.0.2' }),
            makeFlow({ ...denied, srcPod: 'bbb-xyz34', srcIP: '10.0.0.2', dstIP: '10.1.0.3' }),
        ]);

        const anonymous = byKind(graph, 'undisclosed');
        expect(anonymous).toHaveLength(2);
        expect(anonymous.map(n => n.detail).sort()).toEqual(['10.1.0.3', '2 addresses']);
        // Degree 1 each: a leaf asserting only "this workload has traffic to somewhere you
        // cannot see", which is true.
        for (const node of anonymous) {
            expect(graph.edges.filter(e => e.source === node.id || e.target === node.id)).toHaveLength(1);
        }
    });

    // The same real namespace can appear twice — under its name for its allowed flows, among the
    // anonymous nodes for its denied ones. Merging the two by matching addresses would rebuild
    // the IP-to-namespace map the redaction exists to prevent.
    test('an anonymous peer is not merged into a named one sharing its address', async () => {
        const { graph } = await graphFor([
            makeFlow({ srcIP: '10.0.0.1', dstIP: '10.1.0.1', dstNs: 'ns-c', dstPod: '', dstDisclosure: EndpointDisclosure.Flow }),
            makeFlow({
                srcIP: '10.0.0.2', dstIP: '10.1.0.1', dstNs: '', dstPod: '',
                dstDisclosure: EndpointDisclosure.Flow, ingressAction: NetworkPolicyRuleAction.Drop,
            }),
        ]);

        expect(byKind(graph, 'undisclosedNamespace')).toHaveLength(1);
        expect(byKind(graph, 'undisclosed')).toHaveLength(1);
    });

    test('an edge into a collapsed node aggregates the flows behind it', async () => {
        const { graph } = await graphFor([
            makeFlow({ srcIP: '10.0.0.1', dstIP: '10.1.0.1', dstNs: 'ns-c', dstPod: '', dstDisclosure: EndpointDisclosure.Flow }),
            makeFlow({ srcIP: '10.0.0.1', dstIP: '10.1.0.2', dstNs: 'ns-c', dstPod: '', dstDisclosure: EndpointDisclosure.Flow }),
        ]);

        expect(graph.edges).toHaveLength(1);
        // Both peers behind the collapsed node, and the bytes of both flows.
        expect(graph.edges[0].connectionCount).toBe(2);
        expect(graph.edges[0].totalBytesForward).toBe(200);
    });

    // Counting distinct flow.id instead would collapse this edge to one connection and flatten
    // the map's edge widths: nothing in this repo makes the Flow Aggregator populate id, and
    // nothing needs to - FlowStore already deduplicates on connectionKey, so the records that
    // reach buildGraph are one per connection and counting them is the answer.
    test('connections are counted even when the records carry no flow id', async () => {
        const { graph } = await graphFor([
            makeFlow({ id: '', srcIP: '10.0.0.1', dstIP: '10.1.0.1', dstNs: 'ns-c', dstPod: '', dstDisclosure: EndpointDisclosure.Flow }),
            makeFlow({ id: '', srcIP: '10.0.0.1', dstIP: '10.1.0.2', dstNs: 'ns-c', dstPod: '', dstDisclosure: EndpointDisclosure.Flow }),
        ]);

        expect(graph.edges).toHaveLength(1);
        expect(graph.edges[0].connectionCount).toBe(2);
    });

    // The edge's colour is the whole edge's verdict - one drop anywhere on it turns it red - but a
    // tooltip lists one line per policy, and stamping the direction's worst action on every line
    // marked an allow rule as a denial.
    test('each policy line is marked by its own action, not the edge\'s', async () => {
        const { page, graph } = await graphFor([
            makeFlow({ dstPort: 80, egressPolicy: 'allow-egress-from-client', egressAction: NetworkPolicyRuleAction.Allow }),
            makeFlow({ dstPort: 6443, egressPolicy: 'deny-egress-to-apiserver', egressAction: NetworkPolicyRuleAction.Drop }),
        ]);

        expect(graph.edges).toHaveLength(1);
        (page as unknown as { _showTooltip(e: MouseEvent, edge: unknown): void })
            ._showTooltip(new MouseEvent('mousemove'), graph.edges[0]);
        await page.updateComplete;

        const lines = Array.from(page.shadowRoot!.querySelectorAll('#graph-tooltip > div'))
            .map(d => d.textContent ?? '')
            .filter(t => t.includes('Egress:'));
        expect(lines).toEqual([
            '✓ Egress: allow-egress-from-client (Allow)',
            '✗ Egress: deny-egress-to-apiserver (Drop)',
        ]);
    });

    // Form A's tooltip is actionable, so it is worth showing; Form B has no namespace to name,
    // so its lock is a static marker with nothing behind it.
    test('the collapsed namespace carries a tooltip and the anonymous peer does not', async () => {
        const { page } = await graphFor([
            makeFlow({ srcIP: '10.0.0.1', dstIP: '10.1.0.1', dstNs: 'ns-c', dstPod: '', dstDisclosure: EndpointDisclosure.Flow }),
            makeFlow({
                srcIP: '10.0.0.2', dstIP: '10.1.0.3', dstNs: '', dstPod: '',
                dstDisclosure: EndpointDisclosure.Flow, ingressAction: NetworkPolicyRuleAction.Drop,
            }),
        ]);

        // aria-label, not an SVG <title>: the visible tooltip is drawn in the same panel the
        // edge tooltip uses, and a <title> would stack the browser's own on top of it. The
        // label is what a screen reader gets, and it carries the same string.
        const labels = Array.from(page.shadowRoot!.querySelectorAll('#graph-svg g[aria-label]'))
            .map(g => g.getAttribute('aria-label'));
        expect(labels).toHaveLength(1);
        expect(labels[0]).toContain('Workloads in ns-c are not shown');
        expect(labels[0]).toContain('watch on flows or get on flows/identity in ns-c');
        expect(page.shadowRoot!.querySelectorAll('#graph-svg title')).toHaveLength(0);
    });

    // The Service is disclosed by its own namespace, so it can be shown on a destination that is
    // withheld. The map shows it then as it would for a disclosed destination, rather than
    // folding the flow into the endpoint's collapsed namespace.
    test('a Service shown on a withheld destination names the node', async () => {
        const { graph } = await graphFor([
            makeFlow({
                srcIP: '10.0.0.1', dstIP: '10.1.0.1', dstNs: 'ns-c', dstPod: '',
                dstDisclosure: EndpointDisclosure.Flow, destService: 'ns-b/xsvc:http',
            }),
            makeFlow({
                srcIP: '10.0.0.1', dstIP: '10.1.0.2', dstNs: 'ns-c', dstPod: '',
                dstDisclosure: EndpointDisclosure.Flow, svcDisclosure: EndpointDisclosure.Flow,
            }),
        ]);

        const service = graph.nodes.find(n => n.id === 'ns-b/xsvc');
        expect(service).toMatchObject({ kind: 'workload', shortName: 'xsvc', namespace: 'ns-b' });
        // Only the flow whose Service name was cleared, as a withheld Service's is, collapses.
        const collapsed = byKind(graph, 'undisclosedNamespace');
        expect(collapsed).toHaveLength(1);
        expect(collapsed[0].detail).toBe('10.1.0.2');
    });

    // A Service's endpoints can be in another namespace than the Service. The node is the
    // Service, so it sits in the Service's namespace hull.
    test('a Service node is in the Service\'s namespace, not its endpoint\'s', async () => {
        const { graph } = await graphFor([
            makeFlow({ dstNs: 'ns-c', dstPod: 'server-xyz34', destService: 'ns-b/xsvc:http' }),
        ]);

        expect(graph.nodes.find(n => n.id === 'ns-b/xsvc')).toMatchObject({ namespace: 'ns-b', detail: 'ns-b' });
    });

    // A destination outside the Pod network is exported as To External, but the Service the
    // client called is still known, and the Flow List shows it.
    test('a Service in front of a destination outside the Pod network names the node', async () => {
        const external = { flowType: FlowType.ToExternal, dstNs: '', dstPod: '' };
        const { graph } = await graphFor([
            makeFlow({ ...external, dstIP: '172.18.0.2', dstPort: 6443, destService: 'default/kubernetes:https' }),
            makeFlow({ ...external, dstIP: '8.8.8.8', dstPort: 443 }),
        ]);

        expect(graph.nodes.find(n => n.id === 'default/kubernetes'))
            .toMatchObject({ kind: 'workload', shortName: 'kubernetes', namespace: 'default' });
        // Only the flow with no Service goes to External.
        expect(byKind(graph, 'external')).toHaveLength(1);
        expect(graph.edges.filter(e => e.target === 'external')).toHaveLength(1);
    });

    // The latent bug the collapse had to fix regardless: getWorkloadName returns "ns/" for an
    // endpoint with no Pod name and no labels, which used to give a node an empty label sized by
    // textWidth("") — a malformed sliver.
    test('an endpoint with no workload name falls back to its address instead of rendering empty', async () => {
        const { graph } = await graphFor([
            makeFlow({ dstNs: 'ns-b', dstPod: '', dstIP: '10.1.0.8', dstDisclosure: EndpointDisclosure.Identity }),
        ]);

        const named = graph.nodes.find(n => n.id !== 'default/client')!;
        expect(named.kind).toBe('workload');
        expect(named.shortName).toBe('10.1.0.8');
    });
});


const FINITE_COMPLETE = 'event: complete\ndata: {"snapshotCount":2,"maxCount":10000}\n\n';

function finiteResponse(chunks: string[]): Response {
    return new Response(new ReadableStream<Uint8Array>({
        start(c) { c.enqueue(new TextEncoder().encode(chunks.join(''))); c.close(); },
    }));
}

function streamParams(call: unknown[]): URLSearchParams {
    return new URL(call[0] as string, 'http://test').searchParams;
}

/** Hands the page a query and lets the request it causes, and the render after it, land. */
async function setQuery(page: AntreaFlowVisibilityPage, flowQuery: FlowQuery | undefined): Promise<void> {
    page.flowQuery = flowQuery;
    await page.updateComplete;
    await vi.advanceTimersByTimeAsync(0);
    await page.updateComplete;
}

describe('flowQuery', () => {
    test('with no query, the request is a plain live stream and the page owns no query controls', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        const page = await mount(fetchMock);
        const params = streamParams(streamCalls(fetchMock)[0]);
        expect(params.has('source')).toBe(false);
        expect(params.has('follow')).toBe(false);
        expect(params.has('since')).toBe(false);
        expect(params.has('maxCount')).toBe(false);
        expect(page.shadowRoot!.querySelector('#flow-time-range')).toBeNull();
        expect(fetchMock.mock.calls.some(c => String(c[0]).endsWith('/api/v1/settings'))).toBe(false);
        expect(page.shadowRoot!.querySelector('slot[name="controls"]')).not.toBeNull();
    });

    test('a finite query is sent as given and ends without reconnecting', async () => {
        const fetchMock = vi.fn(async () => finiteResponse([flowEventChunk([makeFlow()]), FINITE_COMPLETE]));
        const flowQuery = { source: 'flow-history', follow: false, since: '2026-03-25T00:00:00.000Z', maxCount: 500 };
        const page = await mount(fetchMock, { flowQuery });
        const params = streamParams(streamCalls(fetchMock)[0]);
        expect(params.get('source')).toBe('flow-history');
        expect(params.get('follow')).toBe('false');
        expect(params.get('since')).toBe('2026-03-25T00:00:00.000Z');
        expect(params.get('maxCount')).toBe('500');
        expect(page.shadowRoot!.textContent).toContain('Query complete');
        expect(page.shadowRoot!.textContent).toContain('2 snapshots received');
        await vi.advanceTimersByTimeAsync(30_000);
        expect(streamCalls(fetchMock)).toHaveLength(1);
    });

    test('a finite query has no Pause, Clear or throughput, and says Loading while it runs', async () => {
        const page = await mount(() => new Promise<Response>(() => {}), { flowQuery: { follow: false } });
        const text = page.shadowRoot!.textContent!;
        expect(text).toContain('Loading');
        expect(text).not.toContain('Pause');
        expect(text).not.toContain('Clear');
    });

    test('a finite query colors its status dot by the query state, not by the connection', async () => {
        const dot = (page: AntreaFlowVisibilityPage) => page.shadowRoot!.querySelector('.status-dot')!.getAttribute('style');
        const running = await mount(vi.fn(async () => sseResponse([])), { flowQuery: { follow: false } });
        expect(running.shadowRoot!.textContent).toContain('Loading');
        expect(dot(running)).toContain('antrea-color-warning');
        const done = await mount(vi.fn(async () => finiteResponse([FINITE_COMPLETE])), { flowQuery: { follow: false } });
        expect(done.shadowRoot!.textContent).toContain('Query complete');
        expect(dot(done)).toContain('antrea-color-success');
    });

    test('a new query replaces the open one, and an equal one leaves it alone', async () => {
        const signals: (AbortSignal | null | undefined)[] = [];
        const fetchMock = vi.fn(async (_url: string, init?: RequestInit) => {
            signals.push(init?.signal);
            return finiteResponse([FINITE_COMPLETE]);
        });
        const first = { follow: false, since: '2026-03-25T00:00:00.000Z' };
        const page = await mount(fetchMock, { flowQuery: first });
        expect(streamCalls(fetchMock)).toHaveLength(1);

        await setQuery(page, { ...first });
        expect(streamCalls(fetchMock)).toHaveLength(1);

        await setQuery(page, { follow: false, since: '2026-03-25T00:15:00.000Z' });
        expect(streamCalls(fetchMock)).toHaveLength(2);
        expect(streamParams(streamCalls(fetchMock)[1]).get('since')).toBe('2026-03-25T00:15:00.000Z');
        expect(signals[0]?.aborted).toBe(true);
    });

    test('naming another source is another request', async () => {
        const fetchMock = vi.fn(async () => finiteResponse([FINITE_COMPLETE]));
        const page = await mount(fetchMock, { flowQuery: { follow: false, source: 'a' } });
        await setQuery(page, { follow: false, source: 'b' });
        expect(streamCalls(fetchMock).map(c => streamParams(c).get('source'))).toEqual(['a', 'b']);
    });

    test.each(['list', 'map'] as const)('switching between live and finite in %s view', async view => {
        const fetchMock = vi.fn(async (url: string) => streamParams([url]).get('follow') === 'false'
            ? finiteResponse([flowEventChunk([makeFlow()]), FINITE_COMPLETE])
            : sseResponse([]));
        const page = await mount(fetchMock);
        page.viewMode = view;
        await page.updateComplete;
        await setQuery(page, { follow: false, since: '2026-03-25T00:00:00.000Z' });
        expect(page.shadowRoot!.textContent).toContain('Query complete');
        await setQuery(page, undefined);
        const last = streamParams(streamCalls(fetchMock).at(-1)!);
        expect(last.has('follow')).toBe(false);
        expect(last.has('since')).toBe(false);
        expect(page.shadowRoot!.textContent).not.toContain('snapshots received');
        expect(page.shadowRoot!.textContent).not.toContain('Query complete');
    });

    test('a change of mode loads the selector options for the verb of the new mode', async () => {
        const page = await mount(async () => sseResponse([]), {
            search: '/',
            flowAccess: () => flowAccessFixture({ namespaces: ['team-a'], clusterWide: 'allowed', verb: 'list' }),
        });
        expect((await scopeMenu(page)).map(o => o.value)).not.toContain('__cluster_wide__');
        await setQuery(page, { follow: false });
        expect((await scopeMenu(page)).map(o => o.value)).toContain('__cluster_wide__');
        await setQuery(page, undefined);
        expect((await scopeMenu(page)).map(o => o.value)).not.toContain('__cluster_wide__');
    });

    test.each(['watch', 'list'] as const)(
        'reselects the default scope when only %s is allowed cluster-wide', async verb => {
            const signals: (AbortSignal | null | undefined)[] = [];
            const fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
                signals.push(init?.signal);
                return streamParams([url]).get('follow') === 'false'
                    ? finiteResponse([FINITE_COMPLETE])
                    : sseResponse([]);
            });
            const page = await mount(fetchMock, {
                search: '/',
                flowAccess: () => flowAccessFixture({ namespaces: ['ns-a'], clusterWide: 'allowed', verb }),
            });
            const states: FlowQueryStateDetail[] = [];
            page.addEventListener(FLOW_QUERY_STATE_EVENT, e => states.push((e as CustomEvent<FlowQueryStateDetail>).detail));

            await setQuery(page, { follow: false });
            await setQuery(page, undefined);

            const calls = streamCalls(fetchMock);
            // No intermediate request may reuse the previous mode's default scope.
            expect(calls).toHaveLength(3);
            expect(calls.map(call => {
                const params = streamParams(call);
                return [params.get('follow'), params.get('clusterWide'), params.get('observedNamespace')];
            })).toEqual(verb === 'watch' ? [
                [null, 'true', null], ['false', null, 'ns-a'], [null, 'true', null],
            ] : [
                [null, null, 'ns-a'], ['false', 'true', null], [null, null, 'ns-a'],
            ]);
            expect(signals[0]?.aborted).toBe(true);
            expect(states.map(state => state.state)).not.toContain('error');
            expect(window.location.search).toBe('');
        },
    );

    test.each(['ns-a', '_all'])('keeps the explicit URL scope %s across mode changes', async namespace => {
        const fetchMock = vi.fn(async (url: string) => streamParams([url]).get('follow') === 'false'
            ? finiteResponse([FINITE_COMPLETE])
            : sseResponse([]));
        const page = await mount(fetchMock, {
            search: `/?namespace=${namespace}`,
            flowAccess: () => flowAccessFixture({ namespaces: ['ns-b'], clusterWide: 'allowed', verb: 'watch' }),
        });
        await setQuery(page, { follow: false });
        await setQuery(page, undefined);

        const calls = streamCalls(fetchMock);
        expect(calls).toHaveLength(3);
        for (const call of calls) {
            const params = streamParams(call);
            expect(params.get('clusterWide')).toBe(namespace === '_all' ? 'true' : null);
            expect(params.get('observedNamespace')).toBe(namespace === '_all' ? null : namespace);
        }
        expect(window.location.search).toBe(`?namespace=${namespace}`);
    });

    test('a refused finite query says which verb it needs, and does not retry', async () => {
        const fetchMock = vi.fn(async () => jsonResponse({ message: 'forbidden', retryable: false }, 403));
        const page = await mount(fetchMock, { flowQuery: { follow: false } });
        expect(page.shadowRoot!.textContent).toContain('"list" verb');
        await vi.advanceTimersByTimeAsync(30_000);
        expect(streamCalls(fetchMock)).toHaveLength(1);
    });

    test('a source the backend rejects is reported and not retried', async () => {
        const fetchMock = vi.fn(async () =>
            jsonResponse({ message: 'flow source "x" cannot serve this request', code: 'flow_source_unsupported' }, 400));
        const page = await mount(fetchMock, { flowQuery: { follow: false, source: 'x' } });
        expect(page.shadowRoot!.textContent).toContain('cannot serve this request');
        await vi.advanceTimersByTimeAsync(30_000);
        expect(streamCalls(fetchMock)).toHaveLength(1);
    });

    test('Last Seen carries the date in a finite query', async () => {
        const earlier = new Date(2026, 2, 23, 12, 34, 56);
        const later = new Date(2026, 2, 24, 12, 34, 56);
        const flows = [
            { ...makeFlow({ srcIP: '10.0.0.1' }), endTs: earlier.toISOString() },
            { ...makeFlow({ srcIP: '10.0.0.3' }), endTs: later.toISOString() },
        ];
        const page = await mount(async url => streamParams([url]).get('follow') === 'false'
            ? finiteResponse([flowEventChunk(flows), FINITE_COMPLETE])
            : sseResponse([flowEventChunk(flows)]));
        page.viewMode = 'list';
        await vi.advanceTimersByTimeAsync(1000);
        await page.updateComplete;
        const lastSeen = () => Array.from(page.shadowRoot!.querySelectorAll('tbody tr'))
            .map(row => row.querySelector('td')!.textContent!.trim());
        expect(lastSeen()).toEqual([later.toLocaleTimeString(), earlier.toLocaleTimeString()]);

        await setQuery(page, { follow: false });
        await vi.advanceTimersByTimeAsync(1000);
        await page.updateComplete;
        const cells = lastSeen();
        expect(cells).toHaveLength(2);
        expect(cells[0]).toContain(later.toLocaleDateString());
        expect(cells[1]).toContain(earlier.toLocaleDateString());
        expect(cells[0]).toContain(later.toLocaleTimeString());

        await setQuery(page, undefined);
        await vi.advanceTimersByTimeAsync(1000);
        await page.updateComplete;
        expect(lastSeen()).toEqual([later.toLocaleTimeString(), earlier.toLocaleTimeString()]);
    });
});

describe('flow query state event', () => {
    function collect(page: AntreaFlowVisibilityPage): FlowQueryStateDetail[] {
        const seen: FlowQueryStateDetail[] = [];
        page.addEventListener(FLOW_QUERY_STATE_EVENT, e => seen.push((e as CustomEvent<FlowQueryStateDetail>).detail));
        return seen;
    }

    test('reports loading, then the count and limit at completion', async () => {
        const page = await mount(async () => sseResponse([]));
        const seen = collect(page);
        let finish!: () => void;
        const fetchMock = vi.mocked(fetch);
        fetchMock.mockImplementation(async () => new Promise<Response>(resolve => {
            finish = () => resolve(finiteResponse([flowEventChunk([makeFlow(), makeFlow({ srcIP: '10.0.0.9' })]), FINITE_COMPLETE]));
        }));
        await setQuery(page, { follow: false, since: '2026-03-25T00:00:00.000Z' });
        expect(seen.at(-1)).toEqual({ state: 'loading', snapshotCount: 0, maxCount: 0, truncated: false, evicted: false, droppedCount: 0 });
        finish();
        await vi.advanceTimersByTimeAsync(0);
        await page.updateComplete;
        expect(seen.at(-1)).toEqual({ state: 'complete', snapshotCount: 2, maxCount: 10000, truncated: false, evicted: false, droppedCount: 0 });
    });

    test('reports and warns about a truncated result', async () => {
        const page = await mount(async () => sseResponse([]));
        const seen = collect(page);
        vi.mocked(fetch).mockImplementation(async () => finiteResponse([
            flowEventChunk([makeFlow()]),
            'event: complete\ndata: {"snapshotCount":1,"maxCount":1,"truncated":true}\n\n',
        ]));
        await setQuery(page, { follow: false, maxCount: 1 });
        expect(seen.at(-1)).toMatchObject({ state: 'complete', maxCount: 1, truncated: true, evicted: false });
        expect(page.shadowRoot!.textContent).toContain('Result truncated at 1 records');
        expect(page.shadowRoot!.textContent).toContain('some connections may be missing or show older totals');
        expect(page.shadowRoot!.textContent).toContain('Narrow the query with filters or a shorter time window');
    });

    test('reports the records a source dropped during a finite query', async () => {
        const page = await mount(async () => sseResponse([]));
        const seen = collect(page);
        vi.mocked(fetch).mockImplementation(async () => finiteResponse([
            flowEventChunk([makeFlow()]),
            'event: dropped\ndata: {"droppedCount":5}\n\n',
            FINITE_COMPLETE,
        ]));
        await setQuery(page, { follow: false });
        expect(seen.at(-1)).toMatchObject({ state: 'complete', truncated: false, evicted: false, droppedCount: 5 });
    });

    test('reports and warns when the store evicted connections during a finite query', async () => {
        const page = await mount(async () => sseResponse([]));
        const seen = collect(page);
        (page as unknown as { _store: FlowStore })._store = new FlowStore(1);
        vi.mocked(fetch).mockImplementation(async () => finiteResponse([
            flowEventChunk([makeFlow(), makeFlow({ srcIP: '10.0.0.9' })]), FINITE_COMPLETE,
        ]));
        await setQuery(page, { follow: false });
        expect(seen.at(-1)).toMatchObject({ state: 'complete', truncated: false, evicted: true });
        expect(page.shadowRoot!.textContent).toContain('some connections are missing or show older totals');
        expect(page.shadowRoot!.textContent).not.toContain('oldest entries evicted');
    });

    test('a live stream keeps its own eviction warning and reports no eviction', async () => {
        const page = await mount(async () => sseResponse([]));
        const seen = collect(page);
        (page as unknown as { _store: FlowStore })._store = new FlowStore(1);
        vi.mocked(fetch).mockImplementation(async () =>
            sseResponse([flowEventChunk([makeFlow(), makeFlow({ srcIP: '10.0.0.9' })])]));
        await setQuery(page, { follow: true, requestId: 1 });
        await vi.advanceTimersByTimeAsync(1000);
        await page.updateComplete;
        expect(page.shadowRoot!.textContent).toContain('Store limit reached, oldest entries evicted');
        expect(seen.some(d => d.evicted)).toBe(false);
    });

    test('reports a failed query as an error', async () => {
        const page = await mount(async () => sseResponse([]));
        const seen = collect(page);
        vi.mocked(fetch).mockImplementation(async () => jsonResponse({ message: 'down', retryable: false }, 503));
        await setQuery(page, { follow: false });
        expect(seen.at(-1)?.state).toBe('error');
    });

    test('does not report a failed live stream as a query error', async () => {
        const page = await mount(async () => jsonResponse({ message: 'down', retryable: false }, 503));
        const seen = collect(page);
        await vi.advanceTimersByTimeAsync(1000);
        await page.updateComplete;
        expect(seen.map(d => d.state)).not.toContain('error');
        expect((page as unknown as { _queryState: string })._queryState).toBe('idle');
    });

    test('does not report a refused live stream as a query error', async () => {
        const page = await mount(async () => jsonResponse({ message: 'forbidden', retryable: false }, 403));
        const seen = collect(page);
        await vi.advanceTimersByTimeAsync(1000);
        await page.updateComplete;
        expect(seen.map(d => d.state)).not.toContain('error');
        expect((page as unknown as { _queryState: string })._queryState).toBe('idle');
    });

    test('a new requestId re-runs a query whose other parameters are unchanged', async () => {
        const fetchMock = vi.fn(async () => finiteResponse([flowEventChunk([makeFlow()]), FINITE_COMPLETE]));
        const page = await mount(fetchMock, { flowQuery: { follow: false, requestId: 1 } });
        expect(streamCalls(fetchMock)).toHaveLength(1);

        await setQuery(page, { follow: false, requestId: 1 });
        expect(streamCalls(fetchMock)).toHaveLength(1);

        await setQuery(page, { follow: false, requestId: 2 });
        expect(streamCalls(fetchMock)).toHaveLength(2);
        expect(streamParams(streamCalls(fetchMock)[1]).has('requestId')).toBe(false);
    });

    test('is back to idle once the page returns to live data', async () => {
        const page = await mount(async () => sseResponse([]), { flowQuery: { follow: false } });
        const seen = collect(page);
        vi.mocked(fetch).mockImplementation(async () => sseResponse([]));
        await setQuery(page, undefined);
        expect(seen.at(-1)).toEqual({ state: 'idle', snapshotCount: 0, maxCount: 0, truncated: false, evicted: false, droppedCount: 0 });
    });
});
