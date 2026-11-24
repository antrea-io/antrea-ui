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

import type { SeenNames } from '../lib/seen-names';
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';
import './antrea-flow-visibility-page';
import * as d3 from 'd3';
import { FlowStore } from '../lib/flow-store';
import type { FlowEntry } from '../lib/flow-store';
import type { PositionedNode } from '../lib/service-map-layout';
import { SCOPE_CHANGE_EVENT, SCOPE_CHOSEN_EVENT } from '../lib/namespace-scope';
import type { ScopeChangeDetail, ScopeChosenDetail } from '../lib/namespace-scope';
import type { AntreaFlowVisibilityPage, D3Node } from './antrea-flow-visibility-page';
import { edgeLabelPosition, edgePinPosition, NodeIndex, nodeBoundary, searchFilterOptions, typedNamespace, typedPod, typedService } from './antrea-flow-visibility-page';
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
    srcPort?: number;
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
        transport: { protocolNumber: 6, sourcePort: overrides.srcPort ?? 12345, destinationPort: overrides.dstPort ?? 80 },
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
    test('a scope in the URL opens the stream on connect and reconnect while the access summary is pending', async () => {
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

        el.remove();
        await el.updateComplete;
        document.body.appendChild(el);
        await el.updateComplete;
        expect(streamCalls(fetchMock)).toHaveLength(2);
        expect(streamParams(fetchMock, 1).get('observedNamespace')).toBe('ns-a');

        release();
        await vi.advanceTimersByTimeAsync(0);
        await el.updateComplete;
        expect(streamCalls(fetchMock)).toHaveLength(2);
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

describe('AntreaFlowVisibilityPage — service map zoom controls', () => {
    async function mountMap(flows: Flow[]): Promise<AntreaFlowVisibilityPage> {
        const page = await mount(async () => sseResponse(flows.length ? [flowEventChunk(flows)] : []));
        await page.updateComplete;
        await vi.advanceTimersByTimeAsync(0);
        await vi.advanceTimersByTimeAsync(1000);
        page.viewMode = 'map';
        await page.updateComplete;
        const simulation = (page as unknown as { _simulation: d3.Simulation<PositionedNode, undefined> })._simulation;
        simulation.stop().tick(300);
        simulation.on('end')!.call(simulation);
        await page.updateComplete;
        return page;
    }
    const button = (page: AntreaFlowVisibilityPage, label: string) =>
        page.shadowRoot!.querySelector<HTMLButtonElement>(`.map-controls button[aria-label="${label}"]`)!;
    const atLimit = (page: AntreaFlowVisibilityPage, label: string) =>
        button(page, label).getAttribute('aria-disabled') === 'true';
    // The map's content group is the SVG's only direct <g> child.
    const content = (page: AntreaFlowVisibilityPage) =>
        page.shadowRoot!.querySelector<SVGGElement>('#graph-svg > g')!;
    const scaleOf = (g: SVGGElement) => Number(/scale\(([^)]+)\)/.exec(g.getAttribute('transform') ?? '')?.[1]);

    // jsdom does no SVG layout, so the fit tests assign getBBox themselves: it is what the
    // component reads for the bounds of the content. Remove it again so no other test sees it.
    afterEach(() => {
        delete (SVGGraphicsElement.prototype as { getBBox?: unknown }).getBBox;
    });

    test('offers zoom in, zoom out and fit to view, in that order', async () => {
        const page = await mountMap([makeFlow()]);
        const labels = Array.from(page.shadowRoot!.querySelectorAll('.map-controls button'))
            .map(b => b.getAttribute('aria-label'));
        expect(labels).toEqual(['Reset map', 'Zoom in', 'Zoom out', 'Fit to view']);
    });

    test('observes each new map container once across scope changes', async () => {
        const observers: TestResizeObserver[] = [];
        class TestResizeObserver {
            observe = vi.fn();
            unobserve = vi.fn();
            disconnect = vi.fn();
            constructor(private callback: ResizeObserverCallback) { observers.push(this); }
            resize(width: number, height: number) {
                this.callback([{
                    target: this.observe.mock.calls[0][0], contentRect: { width, height },
                } as ResizeObserverEntry], this);
            }
        }
        const originalResizeObserver = globalThis.ResizeObserver;
        globalThis.ResizeObserver = TestResizeObserver;
        try {
            const page = await mount(async () => sseResponse([]), {
                search: '/', flowAccess: () => flowAccessFixture({ namespaces: [] }),
            });
            page.viewMode = 'map';
            await page.updateComplete;
            expect(page.shadowRoot!.querySelector('.map-container')).toBeNull();
            expect(observers).toHaveLength(0);

            page.locationSearch = '?namespace=ns-a';
            await page.updateComplete;
            const firstHost = page.shadowRoot!.querySelector('.map-container')!;
            expect(observers).toHaveLength(1);
            expect(observers[0].observe).toHaveBeenCalledWith(firstHost);
            observers[0].resize(800, 360);
            await page.updateComplete;
            const firstSvg = page.shadowRoot!.querySelector('#graph-svg')!;
            expect(firstSvg.getAttribute('width')).toBe('800');
            expect(firstSvg.getAttribute('height')).toBe('360');
            expect(observers).toHaveLength(1);
            expect(observers[0].observe).toHaveBeenCalledTimes(1);

            page.locationSearch = '?namespace=ns-b';
            await page.updateComplete;
            expect((page as unknown as { _observedNs: string })._observedNs).toBe('ns-b');
            expect(page.shadowRoot!.querySelector('.map-container')).toBe(firstHost);
            expect(observers).toHaveLength(1);
            expect(observers[0].observe).toHaveBeenCalledTimes(1);
            expect(observers[0].disconnect).not.toHaveBeenCalled();

            page.locationSearch = '';
            await page.updateComplete;
            expect(page.shadowRoot!.querySelector('.map-container')).toBeNull();
            expect(observers[0].disconnect).toHaveBeenCalledTimes(1);

            page.locationSearch = '?namespace=ns-b';
            await page.updateComplete;
            const secondHost = page.shadowRoot!.querySelector('.map-container')!;
            expect(secondHost).not.toBe(firstHost);
            expect(observers).toHaveLength(2);
            expect(observers[1].observe).toHaveBeenCalledWith(secondHost);
            observers[1].resize(640, 280);
            await page.updateComplete;
            const secondSvg = page.shadowRoot!.querySelector<SVGSVGElement>('#graph-svg')!;
            expect(secondSvg.getAttribute('width')).toBe('640');
            expect(secondSvg.getAttribute('height')).toBe('280');
            const zoom = (page as unknown as { _mapZoom: d3.ZoomBehavior<SVGSVGElement, unknown> })._mapZoom;
            expect(zoom.extent().call(secondSvg, undefined, 0, [secondSvg])).toEqual([[0, 0], [640, 280]]);
        } finally {
            globalThis.ResizeObserver = originalResizeObserver;
        }
    });

    test('zoom in and zoom out scale the map and undo each other', async () => {
        const page = await mountMap([makeFlow()]);
        button(page, 'Zoom in').click();
        expect(scaleOf(content(page))).toBeCloseTo(1.5);
        button(page, 'Zoom out').click();
        expect(scaleOf(content(page))).toBeCloseTo(1);
    });

    test('zooming in stops at the 3x limit of wheel zoom', async () => {
        const page = await mountMap([makeFlow()]);
        for (let i = 0; i < 10; i++) button(page, 'Zoom in').click();
        expect(scaleOf(content(page))).toBeCloseTo(3);
    });

    test('zoom in is disabled at the maximum and zoom out at the minimum, and each frees the other', async () => {
        const page = await mountMap([makeFlow()]);
        for (let i = 0; i < 10; i++) button(page, 'Zoom in').click();
        await page.updateComplete;
        expect(atLimit(page, 'Zoom in')).toBe(true);
        expect(atLimit(page, 'Zoom out')).toBe(false);

        for (let i = 0; i < 10; i++) button(page, 'Zoom out').click();
        await page.updateComplete;
        expect(scaleOf(content(page))).toBeCloseTo(0.3);
        expect(atLimit(page, 'Zoom out')).toBe(true);
        expect(atLimit(page, 'Zoom in')).toBe(false);
    });

    test('a button at its limit stays focusable, so a keyboard user does not lose their place', async () => {
        const page = await mountMap([makeFlow()]);
        for (let i = 0; i < 10; i++) button(page, 'Zoom in').click();
        await page.updateComplete;
        expect(button(page, 'Zoom in').disabled).toBe(false);
    });

    test('a rebuild of the map keeps the scale it is drawn at, and zooming continues from it', async () => {
        const page = await mountMap([makeFlow()]);
        for (let i = 0; i < 10; i++) button(page, 'Zoom in').click();
        await page.updateComplete;
        expect(atLimit(page, 'Zoom in')).toBe(true);
        const scale = scaleOf(content(page));

        // The map is updated in place: the content group keeps its transform, so the buttons'
        // idea of the zoom must keep agreeing with what is drawn.
        (page as unknown as { _buildServiceMap(): void })._buildServiceMap();
        await page.updateComplete;
        expect(scaleOf(content(page))).toBeCloseTo(scale);
        expect(atLimit(page, 'Zoom in')).toBe(true);

        button(page, 'Zoom out').click();
        expect(scaleOf(content(page))).toBeCloseTo(scale / 1.5);
    });

    test('returning from the list restores the scale the zoom buttons act on', async () => {
        const page = await mountMap([makeFlow()]);
        for (let i = 0; i < 10; i++) button(page, 'Zoom in').click();
        await page.updateComplete;
        expect(atLimit(page, 'Zoom in')).toBe(true);
        const scale = scaleOf(content(page));

        page.viewMode = 'list';
        await page.updateComplete;
        page.viewMode = 'map';
        await page.updateComplete;
        expect(scaleOf(content(page))).toBeCloseTo(scale);
        expect(atLimit(page, 'Zoom in')).toBe(true);
        expect(atLimit(page, 'Zoom out')).toBe(false);

        button(page, 'Zoom out').click();
        expect(scaleOf(content(page))).toBeCloseTo(scale / 1.5);
    });

    test('both zoom buttons are available at the initial scale', async () => {
        const page = await mountMap([makeFlow()]);
        await page.updateComplete;
        expect(atLimit(page, 'Zoom in')).toBe(false);
        expect(atLimit(page, 'Zoom out')).toBe(false);
    });

    test.each([900, 360])('fit to view centres the content with padding at viewport height %i', async height => {
        const page = await mountMap([makeFlow()]);
        (page as unknown as { _svgHeight: number })._svgHeight = height;
        await page.updateComplete;
        const bounds = { x: 100, y: 50, width: 2000, height: 1000 };
        (SVGGraphicsElement.prototype as unknown as { getBBox: () => unknown }).getBBox = () => bounds;
        button(page, 'Fit to view').click();

        const m = /translate\(([^,]+),([^)]+)\) scale\(([^)]+)\)/.exec(content(page).getAttribute('transform')!)!;
        const [tx, ty, k] = [Number(m[1]), Number(m[2]), Number(m[3])];
        const svg = page.shadowRoot!.querySelector('#graph-svg')!;
        const width = Number(svg.getAttribute('width'));
        expect(Number(svg.getAttribute('height'))).toBe(height);
        expect(k).toBeCloseTo(Math.min((width - 80) / bounds.width, (height - 80) / bounds.height));
        // The centre of the content lands on the centre of the viewport.
        expect(tx + k * (bounds.x + bounds.width / 2)).toBeCloseTo(width / 2);
        expect(ty + k * (bounds.y + bounds.height / 2)).toBeCloseTo(height / 2);
    });

    test('fit to view does not enlarge a small map', async () => {
        const page = await mountMap([makeFlow()]);
        (SVGGraphicsElement.prototype as unknown as { getBBox: () => unknown }).getBBox =
            () => ({ x: 0, y: 0, width: 100, height: 100 });
        button(page, 'Fit to view').click();
        expect(scaleOf(content(page))).toBeCloseTo(1);
    });

    test('resetting a selection twice preserves node positions and the current pan and zoom', async () => {
        const page = await mountMap([makeFlow()]);
        const root = page.shadowRoot!;
        const coordinates = () => Array.from(root.querySelectorAll('.nodes .node'), el => el.getAttribute('transform'));
        const positions = coordinates();
        const svg = root.querySelector<SVGSVGElement>('#graph-svg')!;
        const simulation = (page as unknown as { _simulation: d3.Simulation<D3Node, undefined> })._simulation;
        const restart = vi.spyOn(simulation, 'restart');
        const viewport = d3.zoomIdentity.translate(-185, 72).scale(1.8);
        const zoom = (page as unknown as { _mapZoom: d3.ZoomBehavior<SVGSVGElement, unknown> })._mapZoom;
        d3.select(svg).call(zoom.transform, viewport);
        root.querySelector('.edges path')!.dispatchEvent(new MouseEvent('click'));
        await page.updateComplete;
        expect(root.querySelector('.edge-details')).not.toBeNull();
        button(page, 'Reset map').click();
        await page.updateComplete;
        expect(root.querySelector('.edge-details')).toBeNull();
        expect(coordinates()).toEqual(positions);
        expect(d3.zoomTransform(svg)).toEqual(viewport);
        button(page, 'Reset map').click();
        await page.updateComplete;
        expect(coordinates()).toEqual(positions);
        expect(d3.zoomTransform(svg)).toEqual(viewport);
        expect(restart).not.toHaveBeenCalled();
    });

    test.each(['Zoom in', 'Fit to view'])('reset preserves the viewport during live additions and respects a later %s', async action => {
        const first = makeFlow();
        const page = await mountMap([first]);
        const state = page as unknown as { _entries: FlowEntry[]; _simulation: d3.Simulation<D3Node, undefined>; _mapLayoutPending: boolean };
        const store = new FlowStore();
        store.upsertBatch([first]);
        const addPeer = async (i: number) => {
            store.upsertBatch([makeFlow({ dstPod: `peer${i}-abc12`, dstIP: `10.0.1.${i}` })]);
            state._entries = store.getAll();
            await page.updateComplete;
            expect(state._mapLayoutPending).toBe(true);
        };
        await addPeer(1);
        const bounds = vi.fn(() => ({ x: 0, y: 0, width: 2000, height: 2000 }));
        Object.defineProperty(content(page), 'getBBox', { value: bounds });
        button(page, 'Zoom in').click();
        const beforeReset = content(page).getAttribute('transform');
        button(page, 'Reset map').click();
        await page.updateComplete;
        expect(content(page).getAttribute('transform')).toBe(beforeReset);
        expect(bounds).not.toHaveBeenCalled();

        await addPeer(2);
        button(page, action).click();
        const viewport = content(page).getAttribute('transform');
        expect(viewport).not.toBe(beforeReset);
        bounds.mockClear();
        await addPeer(3);
        const simulation = state._simulation.stop().tick(300);
        simulation.on('end')!.call(simulation);
        expect(bounds).not.toHaveBeenCalled();
        expect(content(page).getAttribute('transform')).toBe(viewport);
    });

    test('after fitting a large map, zooming out is not stopped at the usual minimum', async () => {
        const page = await mountMap([makeFlow()]);
        (SVGGraphicsElement.prototype as unknown as { getBBox: () => unknown }).getBBox =
            () => ({ x: 0, y: 0, width: 20000, height: 10000 });
        button(page, 'Fit to view').click();
        const fitted = scaleOf(content(page));
        expect(fitted).toBeLessThan(0.3);
        button(page, 'Zoom in').click();
        button(page, 'Zoom out').click();
        expect(scaleOf(content(page))).toBeCloseTo(fitted);
    });

    test('the controls are disabled while there is nothing to show', async () => {
        const page = await mountMap([]);
        for (const label of ['Zoom in', 'Zoom out', 'Fit to view']) {
            expect(button(page, label).disabled).toBe(true);
        }
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

    test.each(['keyboard', 'path click', 'label click'])('focuses the details on %s and returns focus to the edge on Escape', async action => {
        const page = await mountWithOneEdge();
        const root = page.shadowRoot!;
        const hit = root.querySelector<SVGPathElement>('.edge-hit-targets [role="button"]')!;
        const label = root.querySelector<SVGGElement>('.edge-labels g')!;
        expect(hit.getAttribute('aria-label')).toBe('Show connections from client to server');
        if (action === 'keyboard') {
            hit.focus();
            hit.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
        } else {
            const target = action === 'path click' ? root.querySelector('.edges path')! : label;
            target.dispatchEvent(new MouseEvent('click', { bubbles: true }));
        }
        await page.updateComplete;
        const close = root.querySelector<HTMLButtonElement>('.edge-details-close')!;
        expect(root.activeElement).toBe(close);
        close.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
        await page.updateComplete;
        expect(root.querySelector('.edge-details')).toBeNull();
        expect(root.activeElement).toBe(hit);
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

        // Simulate the selected edge aging out of the store.
        (page as unknown as { _entries: unknown[] })._entries = [];
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

    test('policy outcomes are applied together and Reset includes every outcome again', async () => {
        const { page, fetchMock } = await mountWithTwoFlows();
        const boxes = Array.from(page.shadowRoot!.querySelectorAll<HTMLInputElement>('.outcome-group input'));
        expect(boxes.map(box => box.value)).toEqual(['allowed', 'denied', 'unprotected']);
        expect(boxes.every(box => !box.checked)).toBe(true);
        const [apply, reset] = page.shadowRoot!.querySelectorAll<HTMLElement>('.filter-actions antrea-button');

        boxes[0].click();
        boxes[2].click();
        await page.updateComplete;
        expect(streamCalls(fetchMock)).toHaveLength(1);
        apply.click();
        await vi.advanceTimersByTimeAsync(0);
        const requests = () => streamCalls(fetchMock).map(([url]) =>
            new URL(String(url), 'http://test').searchParams.get('networkPolicyOutcomes'));
        expect(requests()).toEqual([null, 'allowed,unprotected']);

        boxes[0].click();
        boxes[0].click();
        apply.click();
        await vi.advanceTimersByTimeAsync(0);
        expect(requests()).toHaveLength(2);

        boxes[1].click();
        apply.click();
        await vi.advanceTimersByTimeAsync(0);
        expect(requests()).toEqual([null, 'allowed,unprotected', null]);

        reset.click();
        await vi.advanceTimersByTimeAsync(0);
        expect(requests()).toEqual([null, 'allowed,unprotected', null]);
        expect(boxes.every(box => !box.checked)).toBe(true);
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
    interface TestNode { id: string; shortName: string; namespace: string; kind: string; detail: string; serviceOnly: boolean }
    interface TestEdge { source: string; target: string; connections: unknown[]; totalBytesForward: number }
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
        expect(graph.edges[0].connections).toHaveLength(2);
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
        expect(graph.edges[0].connections).toHaveLength(2);
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
        const labels = Array.from(page.shadowRoot!.querySelectorAll('#graph-svg .node-body[aria-label]'))
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

    // The node a Service names is the Service alone only while nothing else reached it as a
    // workload: that is what the pill shape tells the reader.
    test('a node that is only ever a destination Service is service-only', async () => {
        const { graph } = await graphFor([
            makeFlow({ dstNs: 'ns-b', dstPod: 'server-xyz34', destService: 'ns-b/xsvc:http' }),
        ]);

        expect(graph.nodes.find(n => n.id === 'ns-b/xsvc')?.serviceOnly).toBe(true);
        expect(graph.nodes.find(n => n.id === 'default/client')?.serviceOnly).toBe(false);
    });

    test('a Service node that also sends traffic is not service-only', async () => {
        const { graph } = await graphFor([
            makeFlow({ dstNs: 'ns-b', dstPod: 'server-xyz34', destService: 'ns-b/xsvc:http' }),
            makeFlow({ srcNs: 'ns-b', srcPod: 'xsvc-abc12', srcIP: '10.0.0.9', dstPod: 'other-xyz34' }),
        ]);

        expect(graph.nodes.filter(n => n.id === 'ns-b/xsvc')).toHaveLength(1);
        expect(graph.nodes.find(n => n.id === 'ns-b/xsvc')?.serviceOnly).toBe(false);
    });

    test('a Service node that a flow reaches without the Service is not service-only', async () => {
        const { graph } = await graphFor([
            makeFlow({ dstNs: 'ns-b', dstPod: 'server-xyz34', destService: 'ns-b/xsvc:http' }),
            makeFlow({ dstNs: 'ns-b', dstPod: 'xsvc-abc12', dstIP: '10.1.0.9' }),
        ]);

        expect(graph.nodes.find(n => n.id === 'ns-b/xsvc')?.serviceOnly).toBe(false);
    });

    test('a service-only node is drawn as a pill and a workload as a rounded rectangle', async () => {
        const { page } = await graphFor([
            makeFlow({ dstNs: 'ns-b', dstPod: 'server-xyz34', destService: 'ns-b/xsvc:http' }),
        ]);

        const rect = (name: string) => Array.from(page.shadowRoot!.querySelectorAll('#graph-svg g'))
            .find(g => g.querySelector('rect') && g.querySelector('text')?.textContent === name)
            ?.querySelector('rect');
        const service = rect('xsvc')!;
        expect(Number(service.getAttribute('rx'))).toBe(Number(service.getAttribute('height')) / 2);
        const workload = rect('client')!;
        expect(Number(workload.getAttribute('rx'))).toBeLessThan(Number(workload.getAttribute('height')) / 2);
    });

    test('a service-only node with a very short name is still drawn as a pill, not an ellipse', async () => {
        const { page } = await graphFor([
            makeFlow({ dstNs: 'ns', dstPod: 'server-xyz34', destService: 'ns/a:http' }),
        ]);

        const rect = Array.from(page.shadowRoot!.querySelectorAll('#graph-svg g'))
            .find(g => g.querySelector('rect') && g.querySelector('text')?.textContent === 'a')!
            .querySelector('rect')!;
        const [width, height, rx] = ['width', 'height', 'rx'].map(a => Number(rect.getAttribute(a)));
        // SVG clamps rx to half the width, so a rect narrower than it is tall is an ellipse.
        expect(rx).toBeLessThanOrEqual(width / 2);
        expect(rx).toBe(height / 2);
    });

    test('the legend names the policy verdicts and node shapes', async () => {
        const { page } = await graphFor([makeFlow()]);

        const rows = Array.from(page.shadowRoot!.querySelectorAll('.map-legend .legend-row'))
            .map(r => r.textContent?.trim());
        expect(page.shadowRoot!.querySelector('.outcome-group legend')?.textContent).toBe('Policy verdict');
        expect(rows).toEqual(expect.arrayContaining(['Allowed', 'Denied', 'Unprotected', 'Service (destination only)', 'Workload', 'External', 'Undisclosed (in cluster)']));
    });

    // The node and the edge are the same before and after: the second flow reaches the same node
    // without the Service, so only its shape says it stopped being the Service alone.
    test('the shape follows a Service node that stops being service-only', async () => {
        const encoder = new TextEncoder();
        let controller!: ReadableStreamDefaultController<Uint8Array>;
        const stream = new ReadableStream<Uint8Array>({ start(c) { controller = c; } });
        const page = await mount(async () => new Response(stream, { status: 200 }));
        controller.enqueue(encoder.encode(flowEventChunk([
            makeFlow({ dstNs: 'ns-b', dstPod: 'server-xyz34', destService: 'ns-b/xsvc:http' }),
        ])));
        await vi.advanceTimersByTimeAsync(1000);
        page.viewMode = 'map';
        await page.updateComplete;
        const rx = () => Array.from(page.shadowRoot!.querySelectorAll('#graph-svg g'))
            .find(g => g.querySelector('rect') && g.querySelector('text')?.textContent === 'xsvc')
            ?.querySelector('rect')?.getAttribute('rx');
        const pill = rx();

        controller.enqueue(encoder.encode(flowEventChunk([
            makeFlow({ dstNs: 'ns-b', dstPod: 'xsvc-abc12', dstIP: '10.1.0.9', srcIP: '10.0.0.9' }),
        ])));
        await vi.advanceTimersByTimeAsync(1000);
        await page.updateComplete;

        expect(pill).toBeDefined();
        expect(rx()).toBe('8');
        expect(rx()).not.toBe(pill);
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

    test('uses only the final scope when the host changes mode and URL together', async () => {
        const fetchMock = vi.fn(async (url: string) => streamParams([url]).get('follow') === 'false'
            ? finiteResponse([FINITE_COMPLETE]) : sseResponse([]));
        const page = await mount(fetchMock, {
            search: '/?namespace=_all',
            flowAccess: () => flowAccessFixture({ namespaces: ['ns-a'], clusterWide: 'allowed', verb: 'watch' }),
        });
        page.locationSearch = '';
        page.flowQuery = { follow: false };
        await page.updateComplete;
        await vi.advanceTimersByTimeAsync(0);
        const calls = streamCalls(fetchMock);
        expect(calls).toHaveLength(2);
        expect(streamParams(calls[1]).get('follow')).toBe('false');
        expect(streamParams(calls[1]).get('observedNamespace')).toBe('ns-a');
        expect(streamParams(calls[1]).has('clusterWide')).toBe(false);
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
        expect(page.shadowRoot!.querySelector('.status-row .warn')?.textContent).toBe('5 flow records dropped by the source');
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

describe('nodeBoundary', () => {
    const node = (serviceOnly: boolean): D3Node =>
        ({ id: 'n', shortName: 'xsvc', namespace: 'ns', kind: 'workload', detail: 'ns', serviceOnly, x: 0, y: 0 });

    test('a Service-only node ends an edge on its curved end, a workload on its corner', () => {
        // Aimed at the bounding box's corner, where the pill is curved and the rectangle is not.
        // hw = 28.4, hh = 23, so the pill's end is a semicircle of radius 23 centred at x = 5.4.
        const [px, py] = nodeBoundary(node(true), 28.4, 23, 0);
        expect(Math.hypot(px - 5.4, py)).toBeCloseTo(23, 5);
        const [wx, wy] = nodeBoundary(node(false), 28.4, 23, 0);
        expect([wx, wy]).toEqual([28.4, 23]);
    });

    test('a Service-only node ends an edge on its flat side where the pill is flat', () => {
        // Aimed slightly off the vertical, so that the ray reaches the box inside the flat side.
        // Applying the semicircle there would end the edge at y of about 22.46, inside the side.
        const [, y] = nodeBoundary(node(true), 2, 100, 0);
        expect(y).toBeCloseTo(23, 5);
    });
});

/** Edge labels are drawn only for highlighted, hovered and selected edges. Hovering a node
 * highlights its edges, which reveals them. */
function showEdgeLabels(root: ParentNode, nodeId?: string) {
    const nodes = Array.from(root.querySelectorAll<SVGGElement>('.nodes .node'));
    const node = nodeId ? nodes.find(element => (d3.select(element).datum() as D3Node).id === nodeId) : nodes[0];
    node!.dispatchEvent(new MouseEvent('mouseenter'));
}

describe('edgeLabelPosition', () => {
    const at = (id: string, x: number, y: number): D3Node =>
        ({ id, shortName: id, namespace: 'ns', kind: 'workload', detail: 'ns', serviceOnly: false, x, y });
    const a = at('a', 0, 0);
    const b = at('b', 600, 0);
    const WIDTH = 60;

    test('stays at the midpoint of the edge when no node covers it', () => {
        expect(edgeLabelPosition(a, b, 0, WIDTH, [a, b])).toEqual([300, 0]);
    });

    test('slides along the edge off a node that covers the midpoint', () => {
        const blocker = at('c', 300, 0);
        const [x, y] = edgeLabelPosition(a, b, 0, WIDTH, [a, b, blocker]);
        const hw = nodeBoundary(blocker, 1000, 0, 0)[0] - 300;
        expect(y).toBe(0);
        expect(Math.abs(x - 300)).toBeGreaterThanOrEqual(hw + WIDTH / 2);
        // The nearest uncovered spot, to within one step of the search.
        expect(Math.abs(x - 300)).toBeLessThan(hw + WIDTH / 2 + 4);
    });

    test('finds the same spot through an index of the nodes as through the nodes themselves', () => {
        const nodes = [a, b, at('c', 300, 0), at('d', 340, 5), at('e', 5000, 5000), at('f', -2000, 40)];
        const index = new NodeIndex(nodes);
        for (const offset of [0, 30, -30]) {
            expect(edgeLabelPosition(a, b, offset, WIDTH, index)).toEqual(edgeLabelPosition(a, b, offset, WIDTH, nodes));
        }
        expect(edgeLabelPosition(a, b, 0, WIDTH, index)[0]).not.toBe(300);
    });

    test('stays at the midpoint when no spot on the edge is free', () => {
        const blocker = at('c', 300, 0);
        const wide = { ...blocker, shortName: 'x'.repeat(200) };
        expect(edgeLabelPosition(a, b, 0, WIDTH, [wide])).toEqual([300, 0]);
    });
});

describe('edgePinPosition', () => {
    const node = (kind: D3Node['kind']): D3Node =>
        ({ id: 'n', shortName: 'name', namespace: 'ns', kind, detail: 'ns', serviceOnly: false, x: 0, y: 0 });
    // The node's half width and half height, read off where an edge aimed along each axis ends.
    const halfSize = (d: D3Node) => [nodeBoundary(d, 1000, 0, 0)[0], nodeBoundary(d, 0, 1000, 0)[1]];

    test.each(['workload', 'namespace', 'external', 'undisclosed'] as const)('a %s holds the pin beside its right edge, level with its middle', kind => {
        const d = node(kind);
        const [hw] = halfSize(d);
        const [x, y] = edgePinPosition(d);
        expect(x).toBeGreaterThan(hw);
        expect(x).toBeLessThan(hw + 12);
        expect(y).toBe(0);
    });
});

describe('AntreaFlowVisibilityPage — incremental service map', () => {
    interface MapState {
        _entries: FlowEntry[];
        _svgWidth: number;
        _svgHeight: number;
        _simulation: d3.Simulation<PositionedNode, undefined>;
        _mapZoom: d3.ZoomBehavior<SVGSVGElement, unknown>;
        _buildServiceMap(): void;
    }

    async function mountMap() {
        const page = await mount(async () => sseResponse([]));
        await vi.advanceTimersByTimeAsync(0);
        page.viewMode = 'map';
        await page.updateComplete;
        return { page, state: page as unknown as MapState };
    }

    async function update(page: AntreaFlowVisibilityPage, state: MapState, flows: Flow[]) {
        const store = new FlowStore();
        store.upsertBatch(flows);
        state._entries = store.getAll();
        await page.updateComplete;
    }

    const svgCoordinates = (value: string) => value.match(/[-+]?\d*\.?\d+(?:e[-+]?\d+)?/gi)!.map(Number);

    test.each([
        { dx: 400, dy: 0, reverseNames: false },
        { dx: 400, dy: 0, reverseNames: true },
        { dx: 0, dy: 400, reverseNames: false },
        { dx: 0, dy: 400, reverseNames: true },
        { dx: 360, dy: 240, reverseNames: false },
        { dx: 360, dy: 240, reverseNames: true },
    ])('opposite paths and labels occupy opposite sides at ($dx, $dy), reversed names: $reverseNames', async ({ dx, dy, reverseNames }) => {
        const { page, state } = await mountMap();
        const source = reverseNames ? 'server-xyz34' : 'client-abc12';
        const target = reverseNames ? 'client-abc12' : 'server-xyz34';
        const forward = makeFlow({ srcPod: source, dstPod: target, dstPort: 8080, ingressPolicy: 'allow-client' });
        const reverse = makeFlow({ srcPod: target, dstPod: source, srcIP: '10.0.0.2', dstIP: '10.0.0.1', dstPort: 8080, ingressPolicy: 'allow-server' });
        await update(page, state, [forward, reverse]);
        const simulation = state._simulation.stop().alpha(0);
        const sourceId = reverseNames ? 'default/server' : 'default/client';
        for (const node of simulation.nodes()) {
            node.x = 200 + (node.id === sourceId ? 0 : dx);
            node.y = 200 + (node.id === sourceId ? 0 : dy);
        }
        simulation.on('end')!.call(simulation);
        // Grow the two labels on a settled layout without another simulation tick.
        const denied = (flow: Flow) => ({
            ...flow,
            transport: { ...flow.transport, sourcePort: 54321 },
            k8s: { ...flow.k8s, ingressNetworkPolicyRuleAction: NetworkPolicyRuleAction.Drop },
        });
        await update(page, state, [forward, denied(forward), reverse, denied(reverse)]);
        const root = page.shadowRoot!;
        const paths = Array.from(root.querySelectorAll('.edges path'));
        showEdgeLabels(root);
        const labels = Array.from(root.querySelectorAll('.edge-labels g'));
        expect(paths).toHaveLength(2);
        expect(labels).toHaveLength(2);
        const side = (x: number, y: number) => dx * (y - 200) - dy * (x - 200);
        const curveSides = paths.map(path => {
            const [, , cx, cy] = svgCoordinates(path.getAttribute('d')!);
            return side(cx, cy);
        });
        const labelPositions = labels.map(label => svgCoordinates(label.getAttribute('transform')!));
        paths.forEach((path, i) => {
            const [sx, sy, cx, cy, tx, ty] = svgCoordinates(path.getAttribute('d')!);
            expect(labelPositions[i][0]).toBeCloseTo((sx + 2 * cx + tx) / 4);
            expect(labelPositions[i][1]).toBeCloseTo((sy + 2 * cy + ty) / 4);
            expect(labels[i].textContent).toContain('1 allowed · 1 denied');
        });
        const labelSides = labelPositions.map(([x, y]) => side(x, y));
        expect(curveSides[0] * curveSides[1]).toBeLessThan(0);
        expect(labelSides[0] * labelSides[1]).toBeLessThan(0);
        curveSides.forEach((curveSide, i) => expect(curveSide * labelSides[i]).toBeGreaterThan(0));
        const sizes = labels.map(label => label.querySelector('rect')!);
        const separated = (axis: number, size: string) => Math.abs(labelPositions[0][axis] - labelPositions[1][axis])
            >= sizes.reduce((total, rect) => total + Number(rect.getAttribute(size)), 0) / 2;
        expect(separated(0, 'width') || separated(1, 'height')).toBe(true);
    });

    test('the initial layout stays hidden until settled, then shows its final positions', async () => {
        const { page, state } = await mountMap();
        await update(page, state, [makeFlow()]);
        await page.updateComplete;
        const root = page.shadowRoot!;
        const content = root.querySelector('.map-content')!;
        const node = root.querySelector('.nodes .node')!;
        const initialTransform = node.getAttribute('transform');
        const simulation = state._simulation.stop();
        expect(content.getAttribute('visibility')).toBe('hidden');
        expect(root.querySelector('[role="status"].map-loading')).not.toBeNull();
        expect(root.querySelector<HTMLButtonElement>('[aria-label="Fit to view"]')!.disabled).toBe(true);

        simulation.tick();
        simulation.on('tick')!.call(simulation);
        expect(simulation.alpha()).toBeGreaterThan(simulation.alphaMin());
        expect(content.getAttribute('visibility')).toBe('hidden');
        expect(node.getAttribute('transform')).toBe(initialTransform);

        simulation.tick(300);
        simulation.on('end')!.call(simulation);
        await page.updateComplete;
        const first = simulation.nodes()[0];
        expect(content.getAttribute('visibility')).toBeNull();
        expect(node.getAttribute('transform')).toBe(`translate(${first.x},${first.y})`);
        expect(root.querySelector('.map-loading')).toBeNull();
        expect(root.querySelector<HTMLButtonElement>('[aria-label="Fit to view"]')!.disabled).toBe(false);
    });

    test('edge churn and resize retain node positions, SVG identity and zoom without reheating', async () => {
        const { page, state } = await mountMap();
        const forward = makeFlow();
        const reverse = makeFlow({ srcPod: 'server-xyz34', dstPod: 'client-abc12', srcIP: '10.0.0.2', dstIP: '10.0.0.1' });
        await update(page, state, [forward]);
        const simulation = state._simulation.stop().alpha(0);
        const nodes = [...simulation.nodes()];
        const positions = nodes.map(n => [n.x, n.y]);
        const root = page.shadowRoot!;
        const nodeElements = Array.from(root.querySelectorAll('.nodes .node'));
        const path = root.querySelector('.edges path')!;
        const geometry = path.getAttribute('d');
        showEdgeLabels(root);
        const label = root.querySelector('.edge-labels g')!;
        const labelPosition = label.getAttribute('transform');
        const [sx, sy, cx, cy, tx, ty] = svgCoordinates(geometry!);
        expect(cx).toBeCloseTo((sx + tx) / 2);
        expect(cy).toBeCloseTo((sy + ty) / 2);
        const hull = root.querySelector('.ns-hulls rect');
        expect(hull).not.toBeNull();
        const svg = root.querySelector('svg')!;
        const transform = d3.zoomIdentity.translate(42, 21).scale(1.5);
        (svg as SVGSVGElement & { __zoom: d3.ZoomTransform }).__zoom = transform;
        const restart = vi.spyOn(simulation, 'restart');

        await update(page, state, [forward, reverse]);
        expect(root.querySelectorAll('.edges path')).toHaveLength(2);
        expect(path.getAttribute('d')).not.toBe(geometry);
        expect(label.getAttribute('transform')).not.toBe(labelPosition);
        await update(page, state, [forward]);
        expect(path.getAttribute('d')).toBe(geometry);
        expect(label.getAttribute('transform')).toBe(labelPosition);
        state._svgWidth = 800;
        await page.updateComplete;
        state._svgHeight = 450;
        await page.updateComplete;
        expect(state._mapZoom.extent().call(svg, undefined, 0, [svg])).toEqual([[0, 0], [800, 450]]);

        expect(state._simulation).toBe(simulation);
        expect(simulation.nodes()[0]).toBe(nodes[0]);
        expect(simulation.nodes().map(n => [n.x, n.y])).toEqual(positions);
        Array.from(root.querySelectorAll('.nodes .node')).forEach((node, i) => expect(node).toBe(nodeElements[i]));
        expect(root.querySelector('.edges path')).toBe(path);
        expect(root.querySelector('.ns-hulls rect')).toBe(hull);
        expect(root.querySelector('.map-content')!.getAttribute('transform')).toBe(transform.toString());
        expect(root.querySelectorAll('defs marker')).toHaveLength(3);
        expect(restart).not.toHaveBeenCalled();
        expect(simulation.alpha()).toBe(0);
        restart.mockRestore();
    });

    test.each(['.edges path', '.edge-hit-targets path', '.edge-labels g'])('%s supports edge hover and selection after data refresh', async selector => {
        const { page, state } = await mountMap();
        const other = makeFlow({ dstPod: 'other-xyz34', dstIP: '10.0.0.3' });
        await update(page, state, [makeFlow({ dstPort: 8080 }), other]);
        state._simulation.stop();
        const root = page.shadowRoot!;
        const target = root.querySelector<SVGElement>(selector)!;
        const hit = target.querySelector('rect') ?? target;
        const paths = root.querySelectorAll('.edges path');
        const initialWidth = Number(paths[0].getAttribute('stroke-width'));
        const tooltip = root.querySelector<HTMLElement>('#graph-tooltip')!;
        const onSelected = vi.fn();
        page.addEventListener('antrea-edge-selected', onSelected);
        const transform = root.querySelector('.map-content')!.getAttribute('transform');

        target.dispatchEvent(new MouseEvent('mouseenter', { clientX: 30, clientY: 40 }));
        expect(paths[0].getAttribute('stroke-opacity')).toBe('1');
        expect(Number(paths[0].getAttribute('stroke-width'))).toBe(initialWidth + 0.5);
        expect(paths[1].getAttribute('stroke-opacity')).toBe('0.6');
        expect(Number(paths[1].getAttribute('stroke-width'))).toBe(initialWidth);
        expect(tooltip.style.opacity).toBe('1');
        expect(tooltip.textContent).toContain('TCP(8080)');
        hit.dispatchEvent(new MouseEvent('mousemove', { bubbles: true, clientX: 60, clientY: 70 }));
        expect([tooltip.style.left, tooltip.style.top]).toEqual(['72px', '82px']);
        target.dispatchEvent(new MouseEvent('mouseleave'));
        expect(paths[0].getAttribute('stroke-opacity')).toBe('0.6');
        expect(Number(paths[0].getAttribute('stroke-width'))).toBe(initialWidth);
        expect(tooltip.style.opacity).toBe('0');

        await update(page, state, [
            makeFlow({ dstPort: 443, ingressPolicy: 'deny', ingressAction: NetworkPolicyRuleAction.Drop }),
            makeFlow({ dstPort: 443, srcIP: '10.0.0.9' }), other,
        ]);
        expect(root.querySelector(selector)).toBe(target);
        target.dispatchEvent(new MouseEvent('mouseenter'));
        expect(tooltip.textContent).toContain('2 connections');
        expect(tooltip.textContent).toContain('TCP(443)');
        expect(tooltip.textContent).toContain('1 dropped, 1 with no policy action');
        hit.dispatchEvent(new MouseEvent('click', { bubbles: true }));
        await page.updateComplete;
        expect(root.querySelector('.edge-details')!.textContent).toContain('2 connections');
        expect(root.querySelector('.edge-details')!.textContent).toContain('TCP/443');
        expect(onSelected).toHaveBeenCalledOnce();
        expect(onSelected.mock.calls[0][0].detail).toMatchObject({
            source: 'default/client', target: 'default/server', destPorts: 'TCP(443)',
            ingressPolicyNames: ['deny'],
        });
        expect(root.querySelector('.map-content')!.getAttribute('transform')).toBe(transform);
    });

    test('a mixed edge counts connections and expands to their own destinations, bytes and policy actions', async () => {
        const { page, state } = await mountMap();
        const allowed = makeFlow({ dstPort: 80, egressPolicy: 'allow-client' });
        const dropped = makeFlow({
            dstPort: 53, dstIP: '10.0.0.3', dstPod: 'server-pqrst', egressPolicy: 'allow-client',
            ingressPolicy: 'deny-dns', ingressAction: NetworkPolicyRuleAction.Drop,
        });
        dropped.transport.protocolNumber = 17;
        dropped.stats.octetTotalCount = 2048;
        dropped.reverseStats.octetTotalCount = 0;
        await update(page, state, [allowed, dropped, dropped]);
        const root = page.shadowRoot!;
        const path = root.querySelector('.edges path')!;
        expect(path.getAttribute('marker-end')).toBe('url(#arrowhead-drop)');
        expect(path.getAttribute('stroke-dasharray')).toBeTruthy();
        path.dispatchEvent(new MouseEvent('mouseenter'));
        const tip = root.querySelector('#graph-tooltip')!;
        expect(tip.textContent).toContain('1 allowed, 1 dropped');
        expect(Array.from(root.querySelectorAll('.edge-labels tspan'), line => line.textContent)).toEqual([
            'TCP/80 +1', '1 allowed · 1 denied',
        ]);
        expect(tip.textContent).not.toMatch(/allow-client|deny-dns/);

        path.dispatchEvent(new MouseEvent('click'));
        await page.updateComplete;
        const rows = root.querySelectorAll<HTMLElement>('.edge-connections li');
        expect(rows).toHaveLength(2);
        expect(rows[0].textContent).toContain('TCP/80');
        expect(rows[0].textContent).toContain('default/server-xyz34');
        expect(rows[0].textContent).toContain('100 B forward · 100 B reverse');
        expect(rows[0].textContent).toContain('Egress: allow-client (Allow)');
        expect(rows[0].textContent).not.toContain('deny-dns');
        expect(rows[0].style.getPropertyValue('--outcome-color')).toContain('edge-allow');
        expect(rows[1].textContent).toContain('UDP/53');
        expect(rows[1].textContent).toContain('default/server-pqrst');
        expect(rows[1].textContent).toContain('2.0 KB forward · 0 B reverse');
        expect(rows[1].textContent).toContain('Ingress: deny-dns (Drop)');
        expect(rows[1].textContent).toContain('Egress: allow-client (Allow)');
        expect(rows[1].style.getPropertyValue('--outcome-color')).toContain('edge-drop');
    });

    test('separate client sockets and replicas keep their own source, verdict and byte totals', async () => {
        const { page, state } = await mountMap();
        const first = makeFlow({ id: '', srcPod: 'client-abc12', srcPort: 31001, dstPort: 443, ingressAction: NetworkPolicyRuleAction.Drop });
        const second = makeFlow({ id: '', srcPod: 'client-abc12', srcPort: 31002, dstPort: 443, ingressPolicy: 'allow-client' });
        const replica = makeFlow({ id: '', srcPod: 'client-pqrst', srcIP: '10.0.0.9', srcPort: 31001, dstPort: 443, ingressPolicy: 'allow-replica' });
        first.stats.octetTotalCount = 64;
        second.stats.octetTotalCount = 1024;
        await update(page, state, [first, second, replica, first]);
        const root = page.shadowRoot!;
        expect(root.querySelector('.edge-labels text')!.textContent).toContain('2 allowed · 1 denied');
        root.querySelector('.edges path')!.dispatchEvent(new MouseEvent('click'));
        await page.updateComplete;
        const rows = Array.from(root.querySelectorAll('.edge-connection'), row => row.textContent);
        expect(rows).toHaveLength(3);
        expect(rows[0]).toContain('Source (port 31001): default/client-abc12');
        expect(rows[0]).toContain('Dropped');
        expect(rows[0]).toContain('64 B forward');
        expect(rows[1]).toContain('Source (port 31002): default/client-abc12');
        expect(rows[1]).toContain('1.0 KB forward');
        expect(rows[1]).toContain('allow-client (Allow)');
        expect(rows[2]).toContain('Source (port 31001): default/client-pqrst');
        expect(rows[2]).toContain('allow-replica (Allow)');
    });

    test('a selected edge refreshes its rows and dashing when only outcomes change', async () => {
        const { page, state } = await mountMap();
        const flow = (dstPort: number, ingressAction: NetworkPolicyRuleAction) => makeFlow({
            dstPort, ingressAction, ingressPolicyType: NetworkPolicyType.ACNP,
        });
        const { Allow, Drop, Reject, NoAction } = NetworkPolicyRuleAction;
        const root = page.shadowRoot!;
        await update(page, state, [flow(80, Drop), flow(443, Drop)]);
        const path = root.querySelector('.edges path')!;
        path.dispatchEvent(new MouseEvent('click'));
        await page.updateComplete;
        path.dispatchEvent(new MouseEvent('mouseenter'));
        for (const [first, second, role, label, summary, outcomes] of [
            [Drop, Drop, 'drop', '', '2 dropped', ['Dropped', 'Dropped']],
            [Drop, Allow, 'drop', '1 allowed · 1 denied', '1 allowed, 1 dropped', ['Allowed', 'Dropped']],
            [Reject, Allow, 'drop', '1 allowed · 1 denied', '1 allowed, 1 rejected', ['Allowed', 'Rejected']],
            [Drop, Reject, 'drop', '2 denied', '1 dropped, 1 rejected', ['Rejected', 'Dropped']],
            [Reject, Reject, 'drop', '', '2 rejected', ['Rejected', 'Rejected']],
            [Allow, Allow, 'allow', '', '2 allowed', ['Allowed', 'Allowed']],
            [NoAction, NoAction, 'default', '', '2 with no policy action', ['No policy action', 'No policy action']],
        ] as const) {
            await update(page, state, [flow(80, first), flow(443, second)]);
            expect(root.querySelector('.edges path')).toBe(path);
            expect(path.getAttribute('marker-end')).toBe(`url(#arrowhead-${role})`);
            expect(path.hasAttribute('stroke-dasharray')).toBe(Boolean(label));
            const lines = Array.from(root.querySelectorAll('.edge-labels tspan'), line => line.textContent);
            expect(lines.slice(1)).toEqual(label ? [label] : []);
            expect(root.querySelector('#graph-tooltip')!.textContent).toContain(summary);
            expect(Array.from(root.querySelectorAll('.connection-outcome'), el => el.textContent)).toEqual(outcomes);
        }
    });

    test('a row shows withheld endpoints and policy while retaining the reported reject', async () => {
        const { page, state } = await mountMap();
        await update(page, state, [makeFlow({
            srcIP: '10.0.1.8', srcNs: 'clients', srcPod: '', srcDisclosure: EndpointDisclosure.Flow,
            dstIP: '10.0.2.9', dstNs: '', dstPod: '', dstDisclosure: EndpointDisclosure.Flow,
            ingressAction: NetworkPolicyRuleAction.Reject,
        })]);
        const root = page.shadowRoot!;
        const selected = vi.fn();
        page.addEventListener('antrea-edge-selected', selected);
        root.querySelector('.edges path')!.dispatchEvent(new MouseEvent('click'));
        await page.updateComplete;
        expect(Array.from(root.querySelectorAll('.edge-connection .redacted'), el => el.textContent))
            .toEqual(['clients/⟨hidden⟩', '10.0.2.9']);
        expect(root.querySelector('.edge-connection')!.textContent).toContain('Ingress: Rejected (policy hidden)');
        expect(selected.mock.calls[0][0].detail).toMatchObject({ protected: true, ingressPolicyNames: [] });
    });

    test('large edges page through every connection and clamp the page when flows disappear', async () => {
        const { page, state } = await mountMap();
        const flows = Array.from({ length: 26 }, (_, i) => makeFlow({ dstPort: 8000 + i }));
        await update(page, state, flows);
        const root = page.shadowRoot!;
        root.querySelector('.edges path')!.dispatchEvent(new MouseEvent('click'));
        await page.updateComplete;
        const rows = () => root.querySelectorAll('.edge-connection');
        expect(rows()).toHaveLength(25);
        expect(rows()[0].textContent).toContain('TCP/8000');
        root.querySelector<HTMLButtonElement>('.connection-pages button:last-child')!.click();
        await page.updateComplete;
        expect(rows()).toHaveLength(1);
        expect(rows()[0].textContent).toContain('TCP/8025');
        await update(page, state, flows.slice(0, 2));
        expect(rows()).toHaveLength(2);
        expect(root.querySelector('.connection-pages')).toBeNull();
    });

    test('counter updates retain edge widths and layout forces without DOM writes', async () => {
        const { page, state } = await mountMap();
        const forward = makeFlow();
        const store = new FlowStore();
        const publish = async (flows: Flow[]) => {
            store.upsertBatch(flows);
            state._entries = store.getAll();
            await page.updateComplete;
        };
        await publish([forward, makeFlow({ dstIP: '10.0.0.3', dstPod: 'other-abc12' })]);
        const simulation = state._simulation.stop();
        const forceNames = ['link', 'charge', 'collision', 'cluster'];
        const forces = forceNames.map(name => simulation.force(name));
        const root = page.shadowRoot!;
        const path = root.querySelector('.edges path')!;
        const width = path.getAttribute('stroke-width');
        const mutations: MutationRecord[] = [];
        const observer = new MutationObserver(records => mutations.push(...records));
        observer.observe(root.querySelector('.map-content')!, { subtree: true, attributes: true, childList: true });
        try {
            const updated = { ...forward, stats: { ...forward.stats, octetTotalCount: 2048 } };
            await publish([updated]);
            expect(mutations).toHaveLength(0);

            // A second connection to the same workload only updates the data used by the
            // tooltip; it leaves the width, labels, geometry and layout alone.
            await publish([makeFlow({ srcIP: '10.0.0.9' })]);
            expect(mutations).toHaveLength(0);
            expect(path.getAttribute('stroke-width')).toBe(width);
            forceNames.forEach((name, i) => expect(simulation.force(name)).toBe(forces[i]));
        } finally {
            observer.disconnect();
        }
        path.dispatchEvent(new MouseEvent('mouseenter'));
        const tooltip = root.querySelector('#graph-tooltip')!;
        expect(tooltip.textContent).toContain('2 connections');
        expect(tooltip.textContent).toContain('2.1 KB');
    });

    test('edge labels show an observed protocol and port regardless of arrival order', async () => {
        const { page, state } = await mountMap();
        const tcp = makeFlow({ dstPort: 443 });
        const udp = makeFlow({ dstPort: 53 });
        udp.transport.protocolNumber = 17;
        const icmp = makeFlow({ dstPort: 0 });
        icmp.transport.protocolNumber = 1;
        for (const flows of [[tcp, udp], [udp, tcp], [udp, icmp, tcp, makeFlow({ dstPort: 80 })]]) {
            await update(page, state, flows);
            showEdgeLabels(page.shadowRoot!);
            expect(page.shadowRoot!.querySelector('.edge-labels text')!.textContent)
                .toBe(flows.includes(icmp) ? 'ICMP +3' : 'TCP/443 +1');
        }
    });

    test('updates labels and verdicts without bounding-box reads, including ticks', async () => {
        const { page, state } = await mountMap();
        await update(page, state, [makeFlow()]);
        const simulation = state._simulation.stop();
        const linkForce = simulation.force('link');
        const path = page.shadowRoot!.querySelector('.edges path')!;
        const geometry = path.getAttribute('d');
        const label = page.shadowRoot!.querySelector('.edge-labels g')!;
        const oldWidth = Number(label.querySelector('rect')!.getAttribute('width'));
        const bbox = vi.fn(() => { throw new Error('synchronous label measurement'); });
        Object.defineProperty(label.querySelector('text'), 'getBBox', { value: bbox });
        await update(page, state, [makeFlow({ dstPort: 443, ingressPolicy: 'deny', ingressAction: NetworkPolicyRuleAction.Drop })]);
        expect(page.shadowRoot!.querySelector('.edge-labels g')).toBe(label);
        expect(label.textContent).toBe('TCP/443');
        expect(Number(label.querySelector('rect')!.getAttribute('width'))).toBeGreaterThan(oldWidth);
        expect(page.shadowRoot!.querySelector('.edges path')!.getAttribute('marker-end')).toBe('url(#arrowhead-drop)');
        expect(path.getAttribute('d')).toBe(geometry);
        expect(simulation.force('link')).toBe(linkForce);
        state._simulation.stop().tick(5);
        state._simulation.on('tick')!.call(state._simulation);
        expect(bbox).not.toHaveBeenCalled();
    });

    test('successive node additions preserve old positions and empty graphs can recover', async () => {
        const { page, state } = await mountMap();
        const original = makeFlow();
        await update(page, state, [original]);
        state._simulation.stop().tick(300);
        const first = state._simulation.nodes()[0];
        const position = [first.x, first.y];
        const next = makeFlow({ dstPod: 'new-xyz34', dstIP: '10.0.0.3' });
        await update(page, state, [original, next]);
        expect(state._simulation.alpha()).toBe(0.15);
        expect(state._simulation.force('center')).toBeUndefined();
        state._simulation.stop().tick(100);
        expect([first.x, first.y]).toEqual(position);
        const third = makeFlow({ dstPod: 'third-xyz34', dstIP: '10.0.0.4' });
        await update(page, state, [original, next, third]);
        state._simulation.stop().tick(100);
        expect([first.x, first.y]).toEqual(position);
        expect(state._simulation.nodes().every(n => Number.isFinite(n.x) && Number.isFinite(n.y))).toBe(true);
        await update(page, state, []);
        expect(page.shadowRoot!.querySelectorAll('.node, .edges path, .edge-labels g, .namespace')).toHaveLength(0);
        await update(page, state, [original]);
        expect(page.shadowRoot!.querySelectorAll('.node')).toHaveLength(2);
        expect(state._simulation.alpha()).toBe(1);
    });

    test('an addition during the first layout keeps it running unpinned and centred', async () => {
        const { page, state } = await mountMap();
        const original = makeFlow();
        await update(page, state, [original]);
        state._simulation.stop().tick(20);
        const alpha = state._simulation.alpha();
        expect(alpha).toBeGreaterThan(state._simulation.alphaMin());
        await update(page, state, [original, makeFlow({ dstPod: 'new-xyz34', dstIP: '10.0.0.3' })]);
        const nodes = state._simulation.nodes();
        expect(nodes).toHaveLength(3);
        expect(nodes.every(n => n.fx == null && n.fy == null)).toBe(true);
        expect(state._simulation.force('center')).toBeDefined();
        expect(state._simulation.force('anchorX')).toBeUndefined();
        expect(state._simulation.alpha()).toBe(alpha);
    });

    test('pins from settling additions are released when the layout ends', async () => {
        const { page, state } = await mountMap();
        const original = makeFlow();
        await update(page, state, [original]);
        state._simulation.stop().tick(300);
        await update(page, state, [original, makeFlow({ dstPod: 'new-xyz34', dstIP: '10.0.0.3' })]);
        const [first, second] = state._simulation.nodes();
        const dragged = state._simulation.nodes().find(n => (page.shadowRoot!.querySelector('.nodes .node') as unknown as { __data__: unknown }).__data__ === n)!;
        expect([first.fx, second.fx].every(x => x != null)).toBe(true);
        const down = new MouseEvent('mousedown', { bubbles: true, clientX: 1, clientY: 1 });
        Object.defineProperty(down, 'view', { value: window });
        page.shadowRoot!.querySelector('.nodes .node')!.dispatchEvent(down);
        const move = new MouseEvent('mousemove', { bubbles: true, clientX: 11, clientY: 11 });
        Object.defineProperty(move, 'view', { value: window });
        window.dispatchEvent(move);
        const up = new MouseEvent('mouseup', { bubbles: true, clientX: 11, clientY: 11 });
        Object.defineProperty(up, 'view', { value: window });
        window.dispatchEvent(up);
        await vi.advanceTimersByTimeAsync(0);
        state._simulation.on('end')!.call(state._simulation);
        // Only the node the user dragged stays put.
        expect(state._simulation.nodes().map(n => n.fx != null)).toEqual(
            state._simulation.nodes().map(n => n === dragged));
    });

    test('a graph of only new nodes after an empty list view gets a full layout', async () => {
        const { page, state } = await mountMap();
        const original = makeFlow();
        await update(page, state, [original]);
        state._simulation.stop().tick(300);
        page.viewMode = 'list';
        await page.updateComplete;
        await update(page, state, [makeFlow({ srcPod: 'other-xyz34', srcIP: '10.0.1.1', dstPod: 'another-xyz34', dstIP: '10.0.1.2' })]);
        page.viewMode = 'map';
        await page.updateComplete;
        expect(state._simulation.alpha()).toBe(1);
        expect(state._simulation.force('center')).toBeDefined();
        expect(state._simulation.nodes().every(n => n.fx == null)).toBe(true);
    });

    test.each(['settled', 'running', 'layout-pinned', 'drag-pinned'])('clicks and double clicks preserve %s nodes and the viewport', async phase => {
        const { page, state } = await mountMap();
        const flow = makeFlow();
        await update(page, state, [flow]);
        const simulation = state._simulation.stop();
        const root = page.shadowRoot!;
        const mouse = (type: string, clientX = 40, clientY = 50) => {
            const event = new MouseEvent(type, { bubbles: true, composed: true, clientX, clientY });
            Object.defineProperty(event, 'view', { value: window });
            return event;
        };
        if (phase !== 'running') {
            simulation.tick(300);
            simulation.on('end')!.call(simulation);
        }
        if (phase === 'layout-pinned') {
            await update(page, state, [flow, makeFlow({ dstPod: 'new-xyz34', dstIP: '10.0.0.3' })]);
            simulation.stop();
        }
        const node = root.querySelector('.nodes .node')!;
        if (phase === 'drag-pinned') {
            node.dispatchEvent(mouse('mousedown'));
            window.dispatchEvent(mouse('mousemove', 70, 80));
            window.dispatchEvent(mouse('mouseup', 70, 80));
            await vi.advanceTimersByTimeAsync(0);
        }
        expect(!!(simulation.nodes()[0] as D3Node).userPinned).toBe(phase === 'drag-pinned');
        const positions = () => (simulation.nodes() as D3Node[]).map(n => [n.x, n.y, n.fx, n.fy, n.userPinned]);
        const before = positions();
        const alpha = simulation.alpha();
        const stop = vi.spyOn(simulation, 'stop');
        const restart = vi.spyOn(simulation, 'restart');
        const svg = root.querySelector<SVGSVGElement>('#graph-svg')!;
        const transform = d3.zoomIdentity.translate(25, 30).scale(1.2);
        state._mapZoom.duration(0);
        d3.select(svg).call(state._mapZoom.transform, transform);
        for (let i = 0; i < 2; i++) {
            node.dispatchEvent(mouse('mousedown'));
            window.dispatchEvent(mouse('mousemove'));
            window.dispatchEvent(mouse('mouseup'));
            node.dispatchEvent(mouse('click'));
        }
        node.dispatchEvent(mouse('dblclick'));
        expect(positions()).toEqual(before);
        expect(simulation.alpha()).toBe(alpha);
        expect(stop).not.toHaveBeenCalled();
        expect(restart).not.toHaveBeenCalled();
        expect(d3.zoomTransform(svg)).toEqual(transform);
        expect(root.querySelector('.map-content')!.getAttribute('transform')).toBe(transform.toString());
        if (phase === 'layout-pinned') {
            simulation.on('end')!.call(simulation);
        }
        expect(simulation.nodes().filter(n => n.fx != null || n.fy != null).map(n => n.id))
            .toEqual(phase === 'drag-pinned' ? [simulation.nodes()[0].id] : []);
        svg.dispatchEvent(mouse('dblclick'));
        expect(d3.zoomTransform(svg)).toEqual(transform);
        stop.mockRestore();
        restart.mockRestore();
    });

    test('a touch tap does not change a workload into a user-pinned node', async () => {
        vi.stubGlobal('navigator', { maxTouchPoints: 1 });
        const { page, state } = await mountMap();
        await update(page, state, [makeFlow()]);
        const simulation = state._simulation.stop();
        const before = (simulation.nodes() as D3Node[]).map(n => [n.x, n.y, n.fx, n.fy, n.userPinned]);
        const stop = vi.spyOn(simulation, 'stop');
        const restart = vi.spyOn(simulation, 'restart');
        const node = page.shadowRoot!.querySelector('.nodes .node')!;
        for (const type of ['touchstart', 'touchmove', 'touchend']) {
            const contact = { identifier: 1, target: node, clientX: 40, clientY: 50 } as Touch;
            const active = type === 'touchend' ? [] : [contact];
            node.dispatchEvent(new TouchEvent(type, {
                bubbles: true, cancelable: true,
                touches: active, targetTouches: active, changedTouches: [contact],
            }));
        }
        expect((simulation.nodes() as D3Node[]).map(n => [n.x, n.y, n.fx, n.fy, n.userPinned])).toEqual(before);
        expect(stop).not.toHaveBeenCalled();
        expect(restart).not.toHaveBeenCalled();
        stop.mockRestore();
        restart.mockRestore();
    });

    test('dragging moves only the selected node and does not reheat the simulation', async () => {
        const { page, state } = await mountMap();
        await update(page, state, [makeFlow()]);
        const simulation = state._simulation.stop();
        const [dragged, other] = simulation.nodes();
        const original = [dragged.x!, dragged.y!];
        const otherPosition = [other.x, other.y];
        const restart = vi.spyOn(simulation, 'restart');
        const node = page.shadowRoot!.querySelector('.nodes .node')!;
        const mouse = (type: string, clientX: number, clientY: number) => {
            const event = new MouseEvent(type, { bubbles: true, clientX, clientY });
            // jsdom rejects Vitest's Window proxy as a UIEvent constructor argument.
            Object.defineProperty(event, 'view', { value: window });
            return event;
        };
        node.dispatchEvent(mouse('mousedown', 40, 50));
        window.dispatchEvent(mouse('mousemove', 100, 80));
        window.dispatchEvent(mouse('mouseup', 100, 80));
        expect(dragged.x).toBeCloseTo(original[0] + 60);
        expect(dragged.y).toBeCloseTo(original[1] + 30);
        expect([other.x, other.y]).toEqual(otherPosition);
        expect(node.getAttribute('transform')).toBe(`translate(${dragged.x},${dragged.y})`);
        expect(restart).not.toHaveBeenCalled();
        page.viewMode = 'list';
        await page.updateComplete;
        page.viewMode = 'map';
        await page.updateComplete;
        // The unfinished layout resumes, and the dragged node stays where it was put.
        expect(restart).toHaveBeenCalledTimes(1);
        expect([dragged.fx, dragged.fy]).toEqual([dragged.x, dragged.y]);
        expect([other.x, other.y]).toEqual(otherPosition);
        restart.mockRestore();
        // D3 suppresses the click following a drag until its zero-delay timer fires.
        // Drain it before restoring real timers so later tests can click normally.
        await vi.advanceTimersByTimeAsync(0);
    });

    test.each(['flow count updates', 'edge additions'])('touch dragging survives %s', async change => {
        const { page, state } = await mountMap();
        vi.stubGlobal('navigator', { maxTouchPoints: 1 });
        const forward = makeFlow();
        await update(page, state, [forward]);
        const simulation = state._simulation.stop();
        const [dragged, other] = simulation.nodes();
        const original = [dragged.x!, dragged.y!];
        const otherPosition = [other.x, other.y];
        const restart = vi.spyOn(simulation, 'restart');
        const root = page.shadowRoot!;
        const node = root.querySelector('.nodes .node')!;
        const touch = (type: string, clientX: number, clientY: number) => {
            // jsdom has TouchEvent but no Touch constructor. D3 reads these coordinates
            // from changedTouches, just as it would for a browser's active touch.
            const contact = { identifier: 1, target: node, clientX, clientY } as Touch;
            const active = type === 'touchend' ? [] : [contact];
            node.dispatchEvent(new TouchEvent(type, {
                bubbles: true, cancelable: true,
                touches: active, targetTouches: active, changedTouches: [contact],
            }));
        };
        touch('touchstart', 40, 50);
        touch('touchmove', 60, 60);
        expect(dragged.x).toBeCloseTo(original[0] + 20);
        expect(dragged.y).toBeCloseTo(original[1] + 10);

        const incoming = change === 'edge additions'
            ? makeFlow({ srcPod: 'server-xyz34', dstPod: 'client-abc12', srcIP: '10.0.0.2', dstIP: '10.0.0.1' })
            : makeFlow({ dstPort: 443 });
        await update(page, state, [forward, incoming]);
        expect(root.querySelector('.nodes .node')).toBe(node);
        const paths = Array.from(root.querySelectorAll('.edges path'));
        showEdgeLabels(root);
        const labels = Array.from(root.querySelectorAll('.edge-labels g'));
        expect(paths).toHaveLength(change === 'edge additions' ? 2 : 1);
        const previousPaths = paths.map(path => path.getAttribute('d'));
        const previousLabels = labels.map(label => label.getAttribute('transform'));

        touch('touchmove', 100, 80);
        expect(dragged.x).toBeCloseTo(original[0] + 60);
        expect(dragged.y).toBeCloseTo(original[1] + 30);
        expect([dragged.fx, dragged.fy]).toEqual([dragged.x, dragged.y]);
        expect([other.x, other.y]).toEqual(otherPosition);
        expect(node.getAttribute('transform')).toBe(`translate(${dragged.x},${dragged.y})`);
        paths.forEach((path, i) => expect(path.getAttribute('d')).not.toBe(previousPaths[i]));
        labels.forEach((label, i) => expect(label.getAttribute('transform')).not.toBe(previousLabels[i]));
        expect(restart).not.toHaveBeenCalled();
        restart.mockRestore();

        touch('touchend', 100, 80);
        touch('touchmove', 120, 100);
        expect(dragged.x).toBeCloseTo(original[0] + 60);
        expect(dragged.y).toBeCloseTo(original[1] + 30);
    });

    test.each(['initial layout', 'new nodes'])('returning from list view resumes the unfinished %s without reheating', async phase => {
        const { page, state } = await mountMap();
        const original = makeFlow();
        await update(page, state, [original]);
        if (phase === 'new nodes') {
            state._simulation.stop().tick(300);
            await update(page, state, [original, makeFlow({ dstPod: 'new-xyz34', dstIP: '10.0.0.3' })]);
        }
        const simulation = state._simulation;
        simulation.tick(5);
        const alpha = simulation.alpha();
        const nodes = [...simulation.nodes()];
        const positions = nodes.map(n => [n.x, n.y]);
        const restart = vi.spyOn(simulation, 'restart');
        page.viewMode = 'list';
        await page.updateComplete;
        expect(restart).not.toHaveBeenCalled();
        page.viewMode = 'map';
        await page.updateComplete;

        expect(state._simulation).toBe(simulation);
        expect(restart).toHaveBeenCalledTimes(1);
        expect(simulation.alpha()).toBe(alpha);
        expect(simulation.nodes()).toEqual(nodes);
        expect(simulation.nodes().map(n => [n.x, n.y])).toEqual(positions);
        restart.mockRestore();
        // D3 suppresses the click following a drag until its zero-delay timer fires.
        // Drain it before restoring real timers so later tests can click normally.
        await vi.advanceTimersByTimeAsync(0);
    });

    test('returning from list view restores node positions and the interactive zoom viewport', async () => {
        const { page, state } = await mountMap();
        await update(page, state, [makeFlow()]);
        state._simulation.stop().tick(300);
        const positions = state._simulation.nodes().map(n => [n.x, n.y]);
        const svg = page.shadowRoot!.querySelector<SVGSVGElement>('#graph-svg')!;
        const transform = d3.zoomIdentity.translate(-185, 72).scale(1.8);
        d3.select(svg).call(state._mapZoom.transform, transform);
        const restart = vi.spyOn(state._simulation, 'restart');
        page.viewMode = 'list';
        await page.updateComplete;
        page.viewMode = 'map';
        await page.updateComplete;
        expect(page.shadowRoot!.querySelectorAll('.node')).toHaveLength(2);
        expect(page.shadowRoot!.querySelector('.edges path')!.getAttribute('d')).toMatch(/^M/);
        expect(state._simulation.nodes().map(n => [n.x, n.y])).toEqual(positions);
        const restored = page.shadowRoot!.querySelector<SVGSVGElement>('#graph-svg')!;
        expect(restored).not.toBe(svg);
        expect(d3.zoomTransform(restored)).toEqual(transform);
        expect(restored.querySelector('.map-content')!.getAttribute('transform')).toBe(transform.toString());
        // The visible transform and D3's internal state must agree for the next wheel gesture.
        d3.select(restored).call(state._mapZoom.scaleBy, 1.1);
        expect(d3.zoomTransform(restored).k).toBeCloseTo(1.98);
        expect(restart).not.toHaveBeenCalled();
        restart.mockRestore();
    });
});

describe('AntreaFlowVisibilityPage — namespace aggregation', () => {
    interface AggregateState {
        _entries: FlowEntry[];
        _clusterWide: boolean;
        _edgeLimit: number;
        _busiestEdges: Set<string>;
        _selectedEdgeKey: string | null;
        _hoveredEdgeKey: string | null;
        _expandedNamespaces: Set<string>;
        _mapLayoutPending: boolean;
        _simulation: d3.Simulation<PositionedNode, undefined>;
        _graphRef: {
            nodes: { id: string; kind: string; internal?: { connections: number; denied: number; bytes: number } }[];
            edges: { source: string; target: string; connectionCount: number; totalBytesForward: number; totalBytesReverse: number; protoPorts: Map<number, Set<number>>; ingressPolicyNames: Set<string> }[];
        };
        _toggleNamespace(ns: string): void;
        _buildServiceMap(): void;
    }
    async function aggregate(flows: Flow[], clusterWide = true) {
        const page = await mount(async () => sseResponse([]));
        await vi.advanceTimersByTimeAsync(0);
        const state = page as unknown as AggregateState;
        state._clusterWide = clusterWide;
        page.viewMode = 'map';
        await page.updateComplete;
        const store = new FlowStore();
        store.upsertBatch(flows);
        state._entries = store.getAll();
        await page.updateComplete;
        return { page, state };
    }

    test('the collapse control precedes the zoom controls and folds every expanded namespace', async () => {
        const { page, state } = await aggregate([
            makeFlow({ srcNs: 'a', dstNs: 'b' }),
            makeFlow({ srcNs: 'a', dstNs: 'hidden', dstPort: 81, dstDisclosure: EndpointDisclosure.Flow }),
            makeFlow({ srcNs: 'a', dstNs: '', dstPort: 82, flowType: FlowType.ToExternal }),
        ]);
        await page.updateComplete;
        const root = page.shadowRoot!;
        const button = (label: string) => root.querySelector<HTMLButtonElement>(`.map-controls button[aria-label="${label}"]`)!;
        expect(Array.from(root.querySelectorAll('.map-controls button')).map(button => button.getAttribute('aria-label')))
            .toEqual(['Collapse all namespaces', 'Reset map', 'Zoom in', 'Zoom out', 'Fit to view']);
        expect(root.querySelector('.map-toolbar')!.textContent).not.toContain('Collapse all namespaces');
        expect(button('Collapse all namespaces').disabled).toBe(true);

        const settle = async () => {
            state._simulation.stop().tick(300);
            state._simulation.on('end')!.call(state._simulation);
            await page.updateComplete;
        };
        await settle();
        expect(button('Collapse all namespaces').disabled).toBe(true);
        expect(button('Collapse all namespaces').title).toBe('Collapse all namespaces');
        for (const namespace of ['a', 'b']) {
            state._toggleNamespace(namespace);
            await page.updateComplete;
            await settle();
        }
        expect([...state._expandedNamespaces].sort()).toEqual(['a', 'b']);
        expect(state._graphRef.nodes.some(node => node.kind === 'namespace')).toBe(false);
        expect(state._graphRef.nodes.some(node => node.id === 'undisclosed-namespace:hidden')).toBe(true);
        expect(button('Collapse all namespaces').disabled).toBe(false);

        button('Collapse all namespaces').click();
        await page.updateComplete;
        await settle();
        expect(state._expandedNamespaces.size).toBe(0);
        expect(button('Collapse all namespaces').disabled).toBe(true);
    });

    test('keyboard namespace toggles focus the replacement control unless focus moved during layout', async () => {
        const { page, state } = await aggregate([makeFlow({ srcNs: 'a', dstNs: 'b' })]);
        const root = page.shadowRoot!;
        const settle = async () => {
            state._simulation.stop().tick(300);
            state._simulation.on('end')!.call(state._simulation);
            await page.updateComplete;
        };
        await settle();
        const folded = () => root.querySelector<SVGGElement>('[aria-label="Expand namespace a"]')!;
        const header = () => root.querySelector<SVGTextElement>('.ns-hulls text[role="button"]')!;
        const toggle = async (key: string) => {
            root.activeElement!.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }));
            await page.updateComplete;
        };
        folded().focus();
        await toggle('Enter');
        await settle();
        expect(root.activeElement).toBe(header());
        await toggle(' ');
        await settle();
        expect(root.activeElement).toBe(folded());

        await toggle('Enter');
        const limit = root.querySelector<HTMLSelectElement>('#map-edge-limit')!;
        limit.focus();
        await settle();
        expect(root.activeElement).toBe(limit);
    });

    test.each([true, false])('returning namespaces respect collapse all during eviction (clicked: %s)', async collapse => {
        const original = makeFlow({ srcNs: 'a', dstNs: 'b' });
        const { page, state } = await aggregate([original]);
        const store = new FlowStore(1);
        const publish = async (flow: Flow) => {
            store.upsertBatch([flow]);
            state._entries = store.getAll();
            await page.updateComplete;
            state._simulation.stop().tick(300);
            state._simulation.on('end')!.call(state._simulation);
            await page.updateComplete;
        };
        await publish(original);
        state._toggleNamespace('a');
        await page.updateComplete;
        await publish(makeFlow({ srcNs: 'c', dstNs: 'd', dstPort: 81 }));
        expect(store.hasEvicted()).toBe(true);
        const root = page.shadowRoot!;
        expect(root.querySelector('.ns-hulls text')).toBeNull();
        const button = root.querySelector<HTMLButtonElement>('[aria-label="Collapse all namespaces"]')!;
        expect(button.disabled).toBe(false);
        if (collapse) button.click();
        await publish(original);
        expect(root.querySelector('[aria-label="Expand namespace a"]') !== null).toBe(collapse);
        expect(button.disabled).toBe(collapse);
    });

    test.each(['empty', 'undisclosed'])('the collapse control is disabled for an %s map', async kind => {
        const { page, state } = await aggregate(kind === 'empty' ? [] : [makeFlow({
            srcNs: 'a', dstNs: 'b', srcDisclosure: EndpointDisclosure.Flow, dstDisclosure: EndpointDisclosure.Flow,
        })]);
        state._simulation.stop().tick(300);
        state._simulation.on('end')!.call(state._simulation);
        await page.updateComplete;
        expect(page.shadowRoot!.querySelector<HTMLButtonElement>('.map-controls button[aria-label="Collapse all namespaces"]')!.disabled).toBe(true);
    });

    test.each([1, 3])('expanded namespaces enclose %i workloads and their labels in a rounded rectangle', async count => {
        const namespace = 'a-namespace-with-a-long-name';
        const { page, state } = await aggregate(Array.from({ length: count }, (_, i) => makeFlow({
            srcNs: namespace, dstNs: 'b', srcPod: `a-workload-with-a-long-name-${i}-abc12`, srcIP: `10.0.0.${i + 10}`,
        })));
        state._simulation.stop().tick(300);
        state._simulation.on('end')!.call(state._simulation);
        state._toggleNamespace(namespace);
        await page.updateComplete;
        state._simulation.stop().tick(300);
        state._simulation.on('end')!.call(state._simulation);
        const root = page.shadowRoot!;
        const boundary = root.querySelector('.ns-hulls rect')!;
        expect(boundary).not.toBeNull();
        expect(Number(boundary.getAttribute('rx'))).toBeGreaterThan(0);
        const left = Number(boundary.getAttribute('x')), top = Number(boundary.getAttribute('y'));
        const right = left + Number(boundary.getAttribute('width'));
        const bottom = top + Number(boundary.getAttribute('height'));
        const members = Array.from(root.querySelectorAll<SVGGElement>('.nodes .node'))
            .filter(element => (d3.select(element).datum() as D3Node).namespace === namespace);
        expect(members).toHaveLength(count);
        for (const element of members) {
            const node = d3.select(element).datum() as D3Node;
            expect(Array.from(element.querySelectorAll('text')).map(text => text.textContent)).toEqual([node.shortName]);
            expect(element.querySelector('text')!.getAttribute('dominant-baseline')).toBe('central');
            const body = element.querySelector('rect')!;
            const x = node.x! + Number(body.getAttribute('x'));
            const y = node.y! + Number(body.getAttribute('y'));
            expect(x).toBeGreaterThan(left);
            expect(y).toBeGreaterThan(top + 18);
            expect(x + Number(body.getAttribute('width'))).toBeLessThan(right);
            expect(y + Number(body.getAttribute('height'))).toBeLessThan(bottom);
        }
        const label = root.querySelector('.ns-hulls text')!;
        expect(label.textContent).toContain(namespace);
        expect(Number(label.getAttribute('y'))).toBeGreaterThan(top);
        const legend = Array.from(root.querySelectorAll('.legend-row')).find(row => row.textContent === 'Namespace')!;
        expect(legend.querySelector('rect')!.getAttribute('stroke-dasharray')).toBe(boundary.getAttribute('stroke-dasharray'));
    });

    test.each(['new', 'existing'])('separation includes the %s namespace boundary widened by its title', async mode => {
        const namespace = 'a'.repeat(63);
        const { page, state } = await aggregate([
            makeFlow({ srcNs: namespace, dstNs: 'b', srcPod: 'api-abc12', dstPod: 'web-abc12' }),
        ]);
        const simulation = state._simulation.stop().tick(300);
        simulation.on('end')!.call(simulation);
        state._toggleNamespace(namespace);
        await page.updateComplete;
        if (mode === 'existing') {
            simulation.stop().tick(300);
            simulation.on('end')!.call(simulation);
            state._toggleNamespace('b');
            await page.updateComplete;
        }
        simulation.stop();
        const nodes = simulation.nodes() as D3Node[];
        const member = nodes.find(n => n.namespace === namespace)!;
        const peer = nodes.find(n => n.namespace === 'b')!;
        member.x = 0; member.y = 0;
        peer.x = 250; peer.y = 0;
        const anchor = mode === 'new' ? member : peer;
        const anchorPosition = [anchor.x, anchor.y];
        simulation.on('end')!.call(simulation);

        const root = page.shadowRoot!;
        const hull = Array.from(root.querySelectorAll<SVGGElement>('.ns-hulls g.namespace'))
            .find(element => (d3.select(element).datum() as [string, D3Node[]])[0] === namespace)!
            .querySelector('rect')!;
        const [x, y, width, height] = ['x', 'y', 'width', 'height'].map(name => Number(hull.getAttribute(name)));
        const peerBox = Array.from(root.querySelectorAll<SVGGElement>('.nodes .node'))
            .find(element => d3.select(element).datum() === peer)!.querySelector('rect')!;
        const pw = Number(peerBox.getAttribute('width')), ph = Number(peerBox.getAttribute('height'));
        expect(peer.x! + pw / 2 <= x || peer.x! - pw / 2 >= x + width ||
            peer.y! + ph / 2 <= y || peer.y! - ph / 2 >= y + height).toBe(true);
        expect([anchor.x, anchor.y]).toEqual(anchorPosition);
    });

    test('expansion, collapse and collapse-all keep the old view until settled positions are ready', async () => {
        const { page, state } = await aggregate([
            makeFlow({ srcNs: 'a', dstNs: 'b' }),
            makeFlow({ srcNs: 'a', dstNs: 'b', srcPod: 'second-abc12', srcIP: '10.0.0.3' }),
            makeFlow({ srcNs: 'b', dstNs: 'c', dstPort: 81 }),
        ]);
        const simulation = state._simulation.stop().tick(300);
        simulation.on('end')!.call(simulation);
        const a = simulation.nodes().find(n => n.id === 'namespace:a')!;
        const b = simulation.nodes().find(n => n.id === 'namespace:b')!;
        b.x = a.x! + 10; b.y = a.y! + 10;
        const pinned = simulation.nodes().find(n => n.id === 'namespace:c') as D3Node;
        pinned.userPinned = true;
        pinned.x = pinned.fx = 1500; pinned.y = pinned.fy = 1000;
        const root = page.shadowRoot!;
        const snapshot = () => Array.from(root.querySelectorAll('.nodes .node')).map(node => node.getAttribute('transform'));
        const assertSettled = async () => {
            expect(state._mapLayoutPending).toBe(true);
            const preview = root.querySelector('.map-layout-snapshot')!;
            expect(preview).not.toBeNull();
            expect(preview.getAttribute('aria-hidden')).toBe('true');
            expect(preview.querySelector('[tabindex], [role], .node')).toBeNull();
            expect(preview.querySelector('[pointer-events]:not([pointer-events="none"])')).toBeNull();
            const svg = root.querySelector<SVGSVGElement>('#graph-svg')!;
            const zoom = (page as unknown as { _mapZoom: d3.ZoomBehavior<SVGSVGElement, unknown> })._mapZoom;
            const transform = d3.zoomTransform(svg).translate(5, -3).scale(1.1);
            d3.select(svg).call(zoom.transform, transform);
            expect(preview.getAttribute('transform')).toBe(transform.toString());
            const oldView = preview.outerHTML;
            expect(root.querySelector('.map-content')!.getAttribute('visibility')).toBe('hidden');
            simulation.stop().tick();
            simulation.on('tick')!.call(simulation);
            expect(preview.outerHTML).toBe(oldView);
            expect(root.querySelector('.map-content')!.getAttribute('visibility')).toBe('hidden');
            simulation.tick(300);
            simulation.on('end')!.call(simulation);
            await page.updateComplete;
            expect(root.querySelector('.map-layout-snapshot')).toBeNull();
            expect(state._mapLayoutPending).toBe(false);
            expect(simulation.alpha()).toBeLessThanOrEqual(simulation.alphaMin());
            expect(root.querySelector('.map-content')!.getAttribute('visibility')).toBeNull();
            expect(root.querySelector('.map-content')!.getAttribute('transform')).toBe(transform.toString());
            expect(simulation.nodes().filter(n => n !== pinned).every(n => n.fx == null && n.fy == null)).toBe(true);
            expect([pinned.x, pinned.y, pinned.fx, pinned.fy]).toEqual([1500, 1000, 1500, 1000]);
            const positions = snapshot();
            await vi.advanceTimersByTimeAsync(100);
            expect(snapshot()).toEqual(positions);
        };
        for (const namespace of ['a', 'b', 'a']) {
            state._toggleNamespace(namespace);
            await page.updateComplete;
            await assertSettled();
        }
        const collapseAll = root.querySelector<HTMLButtonElement>('.map-controls button[aria-label="Collapse all namespaces"]')!;
        collapseAll.click();
        await page.updateComplete;
        expect(state._expandedNamespaces.size).toBe(0);
        await assertSettled();
    });

    test('a new workload in an expanded namespace starts near its members and leaves other namespaces clear', async () => {
        const flows = [makeFlow({ srcNs: 'a', dstNs: 'b' }), makeFlow({ srcNs: 'c', dstNs: 'b', srcIP: '10.0.0.3' })];
        const { page, state } = await aggregate(flows);
        const simulation = state._simulation;
        const settle = () => {
            simulation.stop().tick(300);
            simulation.on('end')!.call(simulation);
        };
        settle();
        state._toggleNamespace('a');
        await page.updateComplete;
        settle();
        const byId = (id: string) => simulation.nodes().find(n => n.id === id)!;
        Object.assign(byId('a/client'), { x: 0, y: 0 });
        const folded = Object.assign(byId('namespace:b'), { x: 400, y: 0 });
        Object.assign(byId('namespace:c'), { x: 1000, y: 0 });
        const store = new FlowStore();
        store.upsertBatch([...flows, makeFlow({ srcNs: 'a', srcPod: 'late-abc12', srcIP: '10.0.0.4', dstNs: 'c' })]);
        state._entries = store.getAll();
        await page.updateComplete;
        const late = byId('a/late');
        expect(Math.hypot(late.x!, late.y!)).toBeCloseTo(80);
        settle();
        const hull = page.shadowRoot!.querySelector('.ns-hulls rect')!;
        const [x, y, width, height] = ['x', 'y', 'width', 'height'].map(attr => Number(hull.getAttribute(attr)));
        expect([folded.x, folded.y]).toEqual([400, 0]);
        const box = page.shadowRoot!.querySelector('[aria-label="Expand namespace b"] rect')!;
        const hw = Number(box.getAttribute('width')) / 2, hh = Number(box.getAttribute('height')) / 2;
        expect(folded.x! + hw <= x || folded.x! - hw >= x + width ||
            folded.y! + hh <= y || folded.y! - hh >= y + height).toBe(true);
    });

    test('expanding beside another expanded namespace preserves its workload offsets', async () => {
        const { page, state } = await aggregate([
            makeFlow({ srcNs: 'a', dstNs: 'c' }),
            makeFlow({ srcNs: 'a', dstNs: 'c', srcPod: 'second-abc12', srcIP: '10.0.0.3' }),
            makeFlow({ srcNs: 'b', dstNs: 'c', srcIP: '10.0.0.4' }),
            makeFlow({ srcNs: 'b', dstNs: 'c', srcPod: 'second-abc12', srcIP: '10.0.0.5' }),
        ]);
        const simulation = state._simulation.stop().tick(300);
        simulation.on('end')!.call(simulation);
        state._toggleNamespace('b');
        await page.updateComplete;
        simulation.stop().tick(300);
        simulation.on('end')!.call(simulation);

        const other = (simulation.nodes() as D3Node[]).filter(n => n.namespace === 'b');
        expect(other).toHaveLength(2);
        other.forEach((n, i) => { n.x = 130 + i * 100; n.y = 0; });
        const parent = simulation.nodes().find(n => n.id === 'namespace:a')!;
        parent.x = 0; parent.y = 0;
        const far = simulation.nodes().find(n => n.id === 'namespace:c')!;
        far.x = 2000; far.y = 2000;
        const offsets = () => other.map(n => [n.x! - other[0].x!, n.y! - other[0].y!]);
        const before = offsets();

        state._toggleNamespace('a');
        await page.updateComplete;
        simulation.stop().tick(300);
        simulation.on('end')!.call(simulation);
        expect(offsets()).toEqual(before);
        expect(other.every(n => n.fx == null && n.fy == null)).toBe(true);
    });

    test.each(['workload', 'namespace'])('a new %s during expansion lets unfinished workloads keep settling', async kind => {
        const flows = Array.from({ length: 20 }, (_, i) => makeFlow({
            srcNs: 'a', dstNs: 'b', srcPod: `workload-${i}-abc12`, srcIP: `10.0.0.${i + 10}`,
        }));
        flows.push(makeFlow({ srcNs: 'b', dstNs: 'c', dstPort: 81 }));
        const { page, state } = await aggregate(flows);
        const simulation = state._simulation.stop().tick(300);
        simulation.on('end')!.call(simulation);
        const pinned = simulation.nodes().find(n => n.id === 'namespace:c') as D3Node;
        pinned.userPinned = true;
        pinned.x = pinned.fx = 1500; pinned.y = pinned.fy = 1000;
        state._toggleNamespace('a');
        await page.updateComplete;
        simulation.stop();

        const root = page.shadowRoot!;
        const children = (simulation.nodes() as D3Node[]).filter(n => n.namespace === 'a');
        const originalPositions = children.map(n => [n.x, n.y]);
        const peer = simulation.nodes().find(n => n.id === 'namespace:b')!;
        const peerPosition = [peer.x, peer.y];
        const snapshot = root.querySelector('.map-layout-snapshot')!;
        const oldView = snapshot.outerHTML;
        const zoom = d3.zoomTransform(root.querySelector<SVGSVGElement>('#graph-svg')!);
        const overlaps = () => {
            const boxes = Array.from(root.querySelectorAll<SVGGElement>('.nodes .node'))
                .filter(element => children.includes(d3.select(element).datum() as D3Node))
                .map(element => {
                    const node = d3.select(element).datum() as D3Node;
                    const rect = element.querySelector('rect')!;
                    return { x: node.x!, y: node.y!, width: Number(rect.getAttribute('width')), height: Number(rect.getAttribute('height')) };
                });
            return boxes.flatMap((a, i) => boxes.slice(i + 1).filter(b =>
                Math.abs(a.x - b.x) < (a.width + b.width) / 2 &&
                Math.abs(a.y - b.y) < (a.height + b.height) / 2)).length;
        };
        expect(children).toHaveLength(20);
        expect(overlaps()).toBeGreaterThan(0);
        expect(state._mapLayoutPending).toBe(true);

        simulation.tick(20);
        const alphaBeforeArrival = simulation.alpha();
        const store = new FlowStore();
        store.upsertBatch([...flows, makeFlow({
            srcNs: kind === 'workload' ? 'a' : 'new', dstNs: 'b',
            srcPod: 'late-abc12', srcIP: '10.0.1.1',
        })]);
        state._entries = store.getAll();
        await page.updateComplete;
        simulation.stop();
        expect(simulation.alpha()).toBe(alphaBeforeArrival);
        expect(children.every(n => n.fx == null && n.fy == null)).toBe(true);
        expect([peer.fx, peer.fy]).toEqual(peerPosition);
        expect(root.querySelector('.map-layout-snapshot')).toBe(snapshot);
        expect(snapshot.outerHTML).toBe(oldView);
        expect(root.querySelector('.map-content')!.getAttribute('visibility')).toBe('hidden');

        simulation.tick(300);
        simulation.on('end')!.call(simulation);
        await page.updateComplete;
        expect(children.map(n => [n.x, n.y])).not.toEqual(originalPositions);
        expect(overlaps()).toBe(0);
        // The expanded namespace's boundary has grown over where the peer was, so the peer has
        // moved clear of it, without being left where it started.
        const boundary = root.querySelector('.ns-hulls g.namespace rect')!;
        const [bx, by, bw, bh] = ['x', 'y', 'width', 'height'].map(name => Number(boundary.getAttribute(name)));
        const peerBox = root.querySelectorAll<SVGGElement>('.nodes .node');
        const peerRect = Array.from(peerBox).find(element => d3.select(element).datum() === peer)!.querySelector('rect')!;
        const [pw, ph] = [Number(peerRect.getAttribute('width')), Number(peerRect.getAttribute('height'))];
        const clear = peer.x! + pw / 2 <= bx || peer.x! - pw / 2 >= bx + bw ||
            peer.y! + ph / 2 <= by || peer.y! - ph / 2 >= by + bh;
        expect(clear).toBe(true);
        expect([pinned.x, pinned.y, pinned.fx, pinned.fy]).toEqual([1500, 1000, 1500, 1000]);
        expect(simulation.nodes().filter(n => n !== pinned).every(n => n.fx == null && n.fy == null)).toBe(true);
        expect(d3.zoomTransform(root.querySelector<SVGSVGElement>('#graph-svg')!)).toEqual(zoom);
        expect(state._mapLayoutPending).toBe(false);
        expect(root.querySelector('.map-layout-snapshot')).toBeNull();
        expect(root.querySelector('.map-content')!.getAttribute('visibility')).toBeNull();
    });

    test.each(['empty', 'disconnect'])('cleans up an unfinished namespace transition on %s', async mode => {
        const { page, state } = await aggregate([makeFlow({ srcNs: 'a', dstNs: 'b' })]);
        const simulation = state._simulation.stop().tick(300);
        simulation.on('end')!.call(simulation);
        state._toggleNamespace('a');
        await page.updateComplete;
        expect(page.shadowRoot!.querySelector('.map-layout-snapshot')).not.toBeNull();
        if (mode === 'empty') state._entries = [];
        else page.remove();
        await page.updateComplete;
        expect(page.shadowRoot!.querySelector('.map-layout-snapshot')).toBeNull();
    });

    test('returning from list view finishes an interrupted namespace transition', async () => {
        const { page, state } = await aggregate([makeFlow({ srcNs: 'a', dstNs: 'b' })]);
        const simulation = state._simulation.stop().tick(300);
        simulation.on('end')!.call(simulation);
        state._toggleNamespace('a');
        await page.updateComplete;
        expect(page.shadowRoot!.querySelector('.map-layout-snapshot')).not.toBeNull();
        page.viewMode = 'list';
        await page.updateComplete;
        expect(page.shadowRoot!.querySelector('.map-layout-snapshot')).toBeNull();
        page.viewMode = 'map';
        await page.updateComplete;
        simulation.stop().tick(300);
        simulation.on('end')!.call(simulation);
        await page.updateComplete;
        expect(page.shadowRoot!.querySelector('.map-content')!.getAttribute('visibility')).toBeNull();
        expect(state._mapLayoutPending).toBe(false);
    });

    test('expanding the only namespace keeps its workload at the chosen location', async () => {
        const { page, state } = await aggregate([makeFlow({ srcNs: 'a', dstNs: 'a', dstPod: 'client-abc12' })]);
        const simulation = state._simulation.stop().tick(300);
        simulation.on('end')!.call(simulation);
        const parent = simulation.nodes()[0];
        parent.x = 200; parent.y = 150;
        state._toggleNamespace('a');
        await page.updateComplete;
        expect(simulation.nodes()).toHaveLength(1);
        expect(simulation.nodes()[0]).toMatchObject({ x: 200, y: 150 });
        expect(simulation.force('center')).toBeUndefined();
        state._toggleNamespace('a');
        await page.updateComplete;
        expect(simulation.nodes()[0]).toMatchObject({ id: 'namespace:a', x: 200, y: 150 });
    });

    test.each(['mouse', 'touch'])('%s presses on an expanded namespace do not move or pin its members', async input => {
        vi.stubGlobal('navigator', { maxTouchPoints: 1 });
        const { page, state } = await aggregate([
            makeFlow({ srcNs: 'a', dstNs: 'b' }),
            makeFlow({ srcNs: 'a', dstNs: 'b', srcPod: 'second-abc12', srcIP: '10.0.0.3' }),
        ]);
        const simulation = state._simulation.stop().tick(300);
        simulation.on('end')!.call(simulation);
        state._toggleNamespace('a');
        await page.updateComplete;
        simulation.stop().tick(300);
        simulation.on('end')!.call(simulation);
        const root = page.shadowRoot!;
        const boundary = root.querySelector('.ns-hulls g.namespace rect')!;
        const svg = root.querySelector<SVGSVGElement>('#graph-svg')!;
        const zoom = d3.zoomTransform(svg);
        const positions = () => (simulation.nodes() as D3Node[]).map(n => [n.x, n.y, n.fx, n.fy, n.userPinned]);
        const before = positions();
        const stop = vi.spyOn(simulation, 'stop');
        const restart = vi.spyOn(simulation, 'restart');
        if (input === 'mouse') {
            const mouse = (type: string) => {
                const event = new MouseEvent(type, { bubbles: true, composed: true, clientX: 40, clientY: 50 });
                Object.defineProperty(event, 'view', { value: window });
                return event;
            };
            for (let i = 0; i < 2; i++) {
                boundary.dispatchEvent(mouse('mousedown'));
                window.dispatchEvent(mouse('mousemove'));
                window.dispatchEvent(mouse('mouseup'));
                boundary.dispatchEvent(mouse('click'));
            }
            boundary.dispatchEvent(mouse('dblclick'));
        } else {
            for (const type of ['touchstart', 'touchmove', 'touchend']) {
                const contact = { identifier: 1, target: boundary, clientX: 40, clientY: 50 } as Touch;
                const active = type === 'touchend' ? [] : [contact];
                boundary.dispatchEvent(new TouchEvent(type, {
                    bubbles: true, cancelable: true,
                    touches: active, targetTouches: active, changedTouches: [contact],
                }));
            }
        }
        expect(positions()).toEqual(before);
        expect(state._expandedNamespaces.has('a')).toBe(true);
        expect(d3.zoomTransform(svg)).toEqual(zoom);
        expect(stop).not.toHaveBeenCalled();
        expect(restart).not.toHaveBeenCalled();
        stop.mockRestore();
        restart.mockRestore();
    });

    test.each(['rect', 'text'])('dragging an expanded namespace by its %s moves its members and edges together', async target => {
        const { page, state } = await aggregate([
            makeFlow({ srcNs: 'a', dstNs: 'b' }),
            makeFlow({ srcNs: 'a', dstNs: 'b', srcPod: 'second-abc12', srcIP: '10.0.0.3' }),
        ]);
        const simulation = state._simulation.stop().tick(300);
        simulation.on('end')!.call(simulation);
        state._toggleNamespace('a');
        await page.updateComplete;
        simulation.stop().tick(300);
        simulation.on('end')!.call(simulation);
        await page.updateComplete;
        const root = page.shadowRoot!;
        const group = root.querySelector('.ns-hulls g.namespace')!;
        const boundary = group.querySelector('rect')!;
        const before = new Map(simulation.nodes().map(n => [n.id, { x: n.x!, y: n.y! }]));
        const left = Number(boundary.getAttribute('x')), top = Number(boundary.getAttribute('y'));
        const paths = Array.from(root.querySelectorAll('.edges path'));
        const geometry = paths.map(path => path.getAttribute('d'));
        showEdgeLabels(root, 'namespace:b');
        const labels = Array.from(root.querySelectorAll('.edge-labels g'));
        const labelPositions = labels.map(label => label.getAttribute('transform'));
        const svg = root.querySelector<SVGSVGElement>('#graph-svg')!;
        const zoom = d3.zoomTransform(svg);
        const restart = vi.spyOn(simulation, 'restart');
        const mouse = (type: string, clientX: number, clientY: number) => {
            const event = new MouseEvent(type, { bubbles: true, composed: true, cancelable: true, clientX, clientY });
            Object.defineProperty(event, 'view', { value: window });
            return event;
        };
        group.querySelector(target)!.dispatchEvent(mouse('mousedown', 40, 50));
        window.dispatchEvent(mouse('mousemove', 100, 90));
        window.dispatchEvent(mouse('mouseup', 100, 90));
        // The click emitted at the end of a title drag must not collapse the namespace.
        group.querySelector(target)!.dispatchEvent(mouse('click', 100, 90));
        expect(state._expandedNamespaces.has('a')).toBe(true);
        const nodes = simulation.nodes() as D3Node[];
        for (const node of nodes.filter(n => n.namespace === 'a')) {
            const start = before.get(node.id)!;
            expect(node).toMatchObject({ x: start.x + 60, y: start.y + 40, fx: start.x + 60, fy: start.y + 40, userPinned: true });
        }
        for (const node of nodes.filter(n => n.namespace !== 'a')) {
            const start = before.get(node.id)!;
            expect([node.x, node.y]).toEqual([start.x, start.y]);
        }
        expect(Number(boundary.getAttribute('x'))).toBeCloseTo(left + 60);
        expect(Number(boundary.getAttribute('y'))).toBeCloseTo(top + 40);
        paths.forEach((path, i) => expect(path.getAttribute('d')).not.toBe(geometry[i]));
        labels.forEach((label, i) => expect(label.getAttribute('transform')).not.toBe(labelPositions[i]));
        expect(d3.zoomTransform(svg)).toEqual(zoom);
        expect(restart).not.toHaveBeenCalled();
        restart.mockRestore();
        const moved = simulation.nodes().map(n => [n.x, n.y]);
        await vi.advanceTimersByTimeAsync(100);
        expect(simulation.nodes().map(n => [n.x, n.y])).toEqual(moved);
        group.querySelector('text')!.dispatchEvent(mouse('click', 100, 90));
        await page.updateComplete;
        expect(state._expandedNamespaces.has('a')).toBe(false);
    });

    test('namespace touch dragging includes workloads added during the gesture', async () => {
        vi.stubGlobal('navigator', { maxTouchPoints: 1 });
        const original = makeFlow({ srcNs: 'a', dstNs: 'b' });
        const { page, state } = await aggregate([original]);
        const simulation = state._simulation.stop().tick(300);
        simulation.on('end')!.call(simulation);
        state._toggleNamespace('a');
        await page.updateComplete;
        simulation.stop().tick(300);
        simulation.on('end')!.call(simulation);
        await page.updateComplete;
        const root = page.shadowRoot!;
        const group = root.querySelector('.ns-hulls g.namespace')!;
        const boundary = group.querySelector('rect')!;
        const touch = (type: string, clientX: number, clientY: number) => {
            const contact = { identifier: 1, target: boundary, clientX, clientY } as Touch;
            const active = type === 'touchend' ? [] : [contact];
            boundary.dispatchEvent(new TouchEvent(type, {
                bubbles: true, cancelable: true, touches: active, targetTouches: active, changedTouches: [contact],
            }));
        };
        touch('touchstart', 40, 50);
        touch('touchmove', 60, 70);
        const store = new FlowStore();
        store.upsertBatch([original, makeFlow({ srcNs: 'a', dstNs: 'b', srcPod: 'new-abc12', srcIP: '10.0.0.3' })]);
        state._entries = store.getAll();
        await page.updateComplete;
        simulation.stop();
        expect(root.querySelector('.ns-hulls g.namespace')).toBe(group);
        const members = (simulation.nodes() as D3Node[]).filter(n => n.namespace === 'a');
        expect(members).toHaveLength(2);
        const positions = members.map(n => [n.x!, n.y!]);
        const paths = Array.from(root.querySelectorAll('.edges path'));
        const geometry = paths.map(path => path.getAttribute('d'));
        touch('touchmove', 100, 90);
        members.forEach((node, i) => expect(node).toMatchObject({
            x: positions[i][0] + 40, y: positions[i][1] + 20,
            fx: positions[i][0] + 40, fy: positions[i][1] + 20, userPinned: true,
        }));
        paths.forEach((path, i) => expect(path.getAttribute('d')).not.toBe(geometry[i]));
        touch('touchend', 100, 90);
        simulation.on('end')!.call(simulation);
        expect(members.every(n => n.fx === n.x && n.fy === n.y)).toBe(true);
        const moved = members.map(n => [n.x, n.y]);
        touch('touchmove', 120, 110);
        expect(members.map(n => [n.x, n.y])).toEqual(moved);
    });

    test('folds namespaces, sums directed edges, unions policies and ports, and retains worst verdict', async () => {
        const { page, state } = await aggregate([
            makeFlow({ srcNs: 'a', dstNs: 'b', ingressPolicy: 'allow' }),
            makeFlow({ srcNs: 'a', dstNs: 'b', srcPod: 'other-abc12', dstPort: 443,
                destService: 'b/web:https', ingressPolicy: 'deny', ingressAction: NetworkPolicyRuleAction.Drop }),
            makeFlow({ srcNs: 'b', dstNs: 'a', srcIP: '10.0.0.3' }),
        ]);
        expect(page.shadowRoot!.querySelector<HTMLSelectElement>('#map-edge-limit')!.value).toBe('50');
        expect(state._graphRef.nodes.map(n => n.id).sort()).toEqual(['namespace:a', 'namespace:b']);
        const forward = state._graphRef.edges.find(e => e.source === 'namespace:a')!;
        expect(forward).toMatchObject({ target: 'namespace:b', connectionCount: 2, totalBytesForward: 200, totalBytesReverse: 200 });
        expect(forward.protoPorts.get(6)).toEqual(new Set([80, 443]));
        expect(forward.ingressPolicyNames).toEqual(new Set(['allow', 'deny']));
        const root = page.shadowRoot!;
        const path = root.querySelector('.edges path')!;
        const label = root.querySelector('.edge-labels g')!;
        expect(path.getAttribute('marker-end')).toBe('url(#arrowhead-drop)');
        expect(path.getAttribute('stroke-dasharray')).toBeTruthy();
        expect(label.getAttribute('display')).toBe('none');
        path.dispatchEvent(new MouseEvent('mouseenter'));
        expect(label.getAttribute('display')).toBeNull();
        expect(label.textContent).toContain('1 allowed · 1 denied');
        path.dispatchEvent(new MouseEvent('click'));
        await page.updateComplete;
        expect(root.querySelector('.edge-details-title')!.textContent).toBe('a → b');
        const rows = Array.from(root.querySelectorAll('.edge-connection'), el => el.textContent);
        expect(rows).toHaveLength(2);
        expect(rows.some(row => row.includes('Allowed') && row.includes('a/client-abc12'))).toBe(true);
        expect(rows.some(row => row.includes('Dropped') && row.includes('a/other-abc12'))).toBe(true);
    });

    test('summarizes internal drops including same-workload traffic once, without self-loops', async () => {
        const { page, state } = await aggregate([
            makeFlow({ srcNs: 'a', dstNs: 'a' }),
            makeFlow({ srcNs: 'a', dstNs: 'a', dstPod: 'client-abc12', dstPort: 81,
                ingressAction: NetworkPolicyRuleAction.Reject }),
        ]);
        expect(state._graphRef.nodes).toHaveLength(1);
        expect(state._graphRef.nodes[0].internal).toEqual({ connections: 2, denied: 1, bytes: 400 });
        expect(state._graphRef.edges).toHaveLength(0);
        expect(page.shadowRoot!.querySelector('.nodes')!.textContent).toContain('2 internal · 1 denied');
        const node = page.shadowRoot!.querySelector('.nodes .node')!;
        expect((node.querySelector('rect') as SVGElement).style.stroke).toContain('--antrea-color-edge-drop');
        const labels = node.querySelectorAll('text');
        expect((labels[0] as SVGElement).style.fill).toContain('--antrea-color-map-node-text');
        expect((labels[1] as SVGElement).style.fill).toContain('--antrea-color-edge-drop');
    });

    test('an open namespace tooltip follows live counters and closes when its node disappears', async () => {
        const flow = makeFlow({ srcNs: 'a', dstNs: 'a' });
        const { page, state } = await aggregate([flow]);
        state._simulation.stop();
        const root = page.shadowRoot!;
        const node = root.querySelector<SVGGElement>('.nodes .node')!;
        const enter = d3.select(node).on('mouseenter');
        const tooltip = root.querySelector<HTMLElement>('#graph-tooltip')!;
        node.dispatchEvent(new MouseEvent('mouseenter'));
        node.querySelector('.node-body')!.dispatchEvent(new MouseEvent('mouseenter', { clientX: 30, clientY: 40 }));
        expect(tooltip.style.opacity).toBe('1');
        const store = new FlowStore();
        store.upsertBatch([{
            ...flow,
            stats: { ...flow.stats, octetTotalCount: 2048 },
            reverseStats: { ...flow.reverseStats, octetTotalCount: 1024 },
        }]);
        state._entries = store.getAll();
        await page.updateComplete;
        expect(root.querySelector('.nodes .node')).toBe(node);
        expect(d3.select(node).on('mouseenter')).toBe(enter);
        expect(state._graphRef.nodes[0].internal!.bytes).toBe(3072);
        expect(tooltip.textContent).toContain('1 internal connections, 0 denied, 3.0 KB');
        expect([tooltip.style.left, tooltip.style.top]).toEqual(['42px', '52px']);
        store.upsertBatch([makeFlow({ srcNs: 'a', dstNs: 'a', srcIP: '10.0.0.9', ingressAction: NetworkPolicyRuleAction.Drop })]);
        state._entries = store.getAll();
        await page.updateComplete;
        expect(tooltip.textContent).toContain('2 internal connections, 1 denied');
        state._entries = [];
        await page.updateComplete;
        expect(tooltip.style.opacity).toBe('0');
    });

    test('a counter-only update changes a folded namespace in place', async () => {
        const flow = makeFlow({ srcNs: 'a', dstNs: 'a' });
        const { page, state } = await aggregate([flow]);
        state._simulation.stop();
        const root = page.shadowRoot!;
        const node = root.querySelector<SVGGElement>('.nodes .node')!;
        const children = Array.from(node.children);
        const body = node.querySelector<SVGGElement>('.node-body')!;
        body.focus();
        expect(body.getAttribute('aria-label')).toBe('Expand namespace a');
        expect(body.getAttribute('aria-description')).toBe('1 internal · 0 denied');
        const store = new FlowStore();
        store.upsertBatch([flow, makeFlow({ srcNs: 'a', dstNs: 'a', srcIP: '10.0.0.3' })]);
        state._entries = store.getAll();
        await page.updateComplete;
        expect(root.querySelector('.nodes .node')).toBe(node);
        expect(Array.from(node.children)).toEqual(children);
        expect(node.textContent).toContain('2 internal · 0 denied');
        expect(body.getAttribute('aria-label')).toBe('Expand namespace a');
        expect(body.getAttribute('aria-description')).toBe('2 internal · 0 denied');
        expect(root.activeElement).toBe(body);
    });

    test.each([NetworkPolicyRuleAction.Drop, NetworkPolicyRuleAction.Reject])('refreshes internal denial colors in place for action %s', async action => {
        const allowed = makeFlow({ srcNs: 'a', dstNs: 'a' });
        const denied = makeFlow({ srcNs: 'a', dstNs: 'a', srcIP: '10.0.0.3', ingressAction: action });
        const { page, state } = await aggregate([allowed]);
        const simulation = state._simulation.stop().tick(300);
        simulation.on('end')!.call(simulation);
        const root = page.shadowRoot!;
        const node = root.querySelector<SVGGElement>('.nodes .node')!;
        const children = Array.from(node.children);
        const border = node.querySelector('rect')!.style.stroke;
        const detail = node.querySelector<SVGTextElement>('text.node-detail')!;
        const textColor = detail.style.fill;
        const pin = node.querySelector<SVGTextElement>('.edge-pin')!;
        pin.focus();
        expect(root.activeElement).toBe(pin);

        for (const hasDenials of [true, false]) {
            const store = new FlowStore();
            store.upsertBatch(hasDenials ? [allowed, denied] : [allowed]);
            state._entries = store.getAll();
            await page.updateComplete;
            expect(detail.textContent).toContain(`${hasDenials ? 1 : 0} denied`);
            expect(node.querySelector('rect')!.style.stroke).toContain(hasDenials ? '--antrea-color-edge-drop' : border);
            expect(detail.style.fill).toContain(hasDenials ? '--antrea-color-edge-drop' : textColor);
            expect(Array.from(node.children)).toEqual(children);
            expect(root.activeElement).toBe(pin);
        }
    });

    describe('dragging', () => {
        const mouse = (type: string, clientX: number, clientY: number) => {
            const event = new MouseEvent(type, { bubbles: true, composed: true, cancelable: true, clientX, clientY });
            Object.defineProperty(event, 'view', { value: window });
            return event;
        };
        const press = (target: Element, to: [number, number]) => {
            target.dispatchEvent(mouse('mousedown', 40, 50));
            window.dispatchEvent(mouse('mousemove', ...to));
            window.dispatchEvent(mouse('mouseup', ...to));
            target.dispatchEvent(mouse('click', ...to));
        };

        test('rendering a reused namespace hull in scoped mode removes its cluster-wide drag handler', async () => {
            const { page, state } = await aggregate([makeFlow({ srcNs: 'a', dstNs: 'b' })]);
            const simulation = state._simulation.stop().tick(300);
            simulation.on('end')!.call(simulation);
            state._toggleNamespace('a');
            await page.updateComplete;
            const root = page.shadowRoot!;
            const hull = root.querySelector<SVGGElement>('.ns-hulls g.namespace')!;
            expect(d3.select(hull).on('mousedown.drag')).toBeDefined();
            // Exercise the D3 join on the existing SVG without opening a new flow stream.
            state._clusterWide = false;
            state._expandedNamespaces = new Set();
            state._buildServiceMap();
            expect(root.contains(hull)).toBe(true);
            expect(d3.select(hull).on('mousedown.drag')).toBeUndefined();
        });

        test('dragging the namespace background pans the map after the user switches scope', async () => {
            const fetchMock = vi.fn(async (url: string) => sseResponse([flowEventChunk([
                makeFlow({ srcNs: 'a', dstNs: url.includes('observedNamespace=a') ? 'a' : 'b' }),
            ])]));
            const page = await mount(fetchMock, {
                search: '/?namespace=_all',
                flowAccess: () => flowAccessFixture({ namespaces: ['a', 'b'], clusterWide: 'allowed' }),
            });
            page.viewMode = 'map';
            await vi.advanceTimersByTimeAsync(1000);
            await page.updateComplete;
            const root = page.shadowRoot!;
            const settle = () => {
                const simulation = (page as unknown as AggregateState)._simulation.stop().tick(300);
                simulation.on('end')!.call(simulation);
                return simulation;
            };
            settle();
            root.querySelector('[aria-label="Expand namespace a"]')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
            await page.updateComplete;
            settle();
            expect(root.querySelector('.ns-hulls g.namespace')).not.toBeNull();

            await chooseScope(page, 'a');
            await vi.advanceTimersByTimeAsync(1000);
            await page.updateComplete;
            const calls = streamCalls(fetchMock);
            expect(calls).toHaveLength(2);
            expect(streamParams(calls[0]).get('clusterWide')).toBe('true');
            expect(streamParams(calls[1]).get('observedNamespace')).toBe('a');
            const simulation = settle();
            const boundary = root.querySelector('.ns-hulls g.namespace rect')!;
            const svg = root.querySelector<SVGSVGElement>('#graph-svg')!;
            const before = d3.zoomTransform(svg);
            const nodes = simulation.nodes() as D3Node[];
            const positions = () => nodes.map(n => [n.x, n.y, n.fx, n.fy, n.userPinned]);
            const original = positions();
            press(boundary, [100, 90]);
            expect(d3.zoomTransform(svg)).toMatchObject({ x: before.x + 60, y: before.y + 40, k: before.k });
            expect(positions()).toEqual(original);
            await vi.advanceTimersByTimeAsync(0);
        });

        test.each([
            { scale: 0.5, distance: 2, drag: false }, { scale: 2, distance: 4, drag: true },
        ])('uses client-pixel slop at zoom $scale for a $distance-pixel gesture', async ({ scale, distance, drag }) => {
            const { page, state } = await aggregate([makeFlow({ srcNs: 'a', dstNs: 'b' })]);
            const simulation = state._simulation.stop().tick(300);
            simulation.on('end')!.call(simulation);
            const root = page.shadowRoot!;
            const svg = root.querySelector<SVGSVGElement>('#graph-svg')!;
            const zoom = (page as unknown as { _mapZoom: d3.ZoomBehavior<SVGSVGElement, unknown> })._mapZoom;
            d3.select(svg).call(zoom.transform, d3.zoomIdentity.scale(scale));
            // jsdom lacks SVG coordinate conversion; provide the zoomed container's matrix so
            // D3 delivers local drag coordinates while its click suppression uses client pixels.
            Object.defineProperty(svg, 'createSVGPoint', { value: () => ({
                x: 0, y: 0,
                matrixTransform(matrix: { a: number; d: number }) { return { x: this.x * matrix.a, y: this.y * matrix.d }; },
            }) });
            const node = root.querySelector<SVGGElement>('.nodes .node')!;
            Object.defineProperty(node.parentNode, 'getScreenCTM', { value: () => ({
                inverse: () => ({ a: 1 / scale, d: 1 / scale }),
            }) });
            const datum = d3.select(node).datum() as D3Node;
            const position = [datum.x!, datum.y!];
            try {
                press(node.querySelector('[role="button"]')!, [40 + distance, 50]);
                expect(!!datum.userPinned).toBe(drag);
                expect([datum.x, datum.y]).toEqual([position[0] + (drag ? distance / scale : 0), position[1]]);
                expect(state._expandedNamespaces.has(datum.namespace)).toBe(!drag);
            } finally {
                await vi.advanceTimersByTimeAsync(0);
            }
        });

        test('a press that moves a pixel or two still expands a folded namespace without pinning it', async () => {
            const { page, state } = await aggregate([makeFlow({ srcNs: 'a', dstNs: 'b' })]);
            const simulation = state._simulation.stop();
            const node = page.shadowRoot!.querySelector('.nodes .node')!;
            const pinned = () => (simulation.nodes() as D3Node[]).filter(n => n.userPinned || n.fx != null);
            press(node.querySelector('[role="button"]')!, [42, 51]);
            expect(pinned()).toEqual([]);
            expect(state._expandedNamespaces.size).toBe(1);
        });

        test('moving past the click distance pins the node and does not expand it', async () => {
            const { page, state } = await aggregate([makeFlow({ srcNs: 'a', dstNs: 'b' })]);
            const simulation = state._simulation.stop();
            const node = page.shadowRoot!.querySelector('.nodes .node')!;
            press(node, [70, 80]);
            expect((simulation.nodes() as D3Node[]).filter(n => n.userPinned)).toHaveLength(1);
            expect(state._expandedNamespaces.size).toBe(0);
        });

        test('a press that moves a pixel or two on a namespace boundary pins no workload', async () => {
            const { page, state } = await aggregate([makeFlow({ srcNs: 'a', dstNs: 'b' })]);
            const simulation = state._simulation.stop().tick(300);
            simulation.on('end')!.call(simulation);
            state._toggleNamespace('a');
            await page.updateComplete;
            simulation.stop().tick(300);
            simulation.on('end')!.call(simulation);
            await page.updateComplete;
            const boundary = page.shadowRoot!.querySelector('.ns-hulls g.namespace rect')!;
            boundary.dispatchEvent(mouse('mousedown', 40, 50));
            window.dispatchEvent(mouse('mousemove', 42, 51));
            window.dispatchEvent(mouse('mouseup', 42, 51));
            expect((simulation.nodes() as D3Node[]).filter(n => n.userPinned || n.fx != null)).toEqual([]);
        });

        test.each(['filter', 'clear'])('a %s in list view drops drag positions from the old data', async action => {
            const flow = makeFlow();
            const page = await mount(async () => sseResponse([flowEventChunk([flow])]));
            await vi.advanceTimersByTimeAsync(1000);
            page.viewMode = 'map';
            await page.updateComplete;
            const state = page as unknown as AggregateState;
            const settle = () => {
                const sim = state._simulation.stop().tick(300);
                sim.on('end')!.call(sim);
            };
            settle();
            const root = page.shadowRoot!;
            const node = root.querySelector('.nodes .node')!;
            const old = d3.select(node).datum() as D3Node;
            press(node, [2040, 2050]);
            await vi.advanceTimersByTimeAsync(0);
            expect(old.userPinned).toBe(true);
            page.viewMode = 'list';
            await page.updateComplete;
            const buttons = root.querySelectorAll<HTMLElement>('.filter-actions antrea-button');
            if (action === 'filter') {
                root.querySelector<HTMLButtonElement>('.multiselect-btn')!.click();
                await page.updateComplete;
                toggleOption(page, 'default');
                await page.updateComplete;
                buttons[0].click();
                await vi.advanceTimersByTimeAsync(1000);
            } else {
                buttons[3].click();
                await page.updateComplete;
                const store = new FlowStore();
                store.upsertBatch([flow]);
                state._entries = store.getAll();
            }

            page.viewMode = 'map';
            await page.updateComplete;
            settle();
            const current = state._simulation.nodes().find(n => n.id === old.id) as D3Node;
            expect(current).toBeDefined();
            expect(current.userPinned).toBeFalsy();
            expect(current.fx == null && current.fy == null).toBe(true);
            expect([current.x, current.y]).not.toEqual([old.x, old.y]);
        });

        test('reset remembers a folded position across eviction and a drag of returning members', async () => {
            const flow = makeFlow({ srcNs: 'a', dstNs: 'b' });
            const { page, state } = await aggregate([flow]);
            const settle = async () => {
                const sim = state._simulation.stop().tick(300);
                sim.on('end')!.call(sim);
                await page.updateComplete;
            };
            await settle();
            const find = (id: string) => state._simulation.nodes().find(n => n.id === id)!;
            Object.assign(find('namespace:a'), { x: -1000, y: 0 });
            state._toggleNamespace('a');
            await page.updateComplete;
            await settle();
            state._entries = [];
            await page.updateComplete;
            const store = new FlowStore();
            store.upsertBatch([flow]);
            state._entries = store.getAll();
            await page.updateComplete;
            await settle();
            const root = page.shadowRoot!;
            const hull = root.querySelector('.ns-hulls g.namespace rect')!;
            press(hull, [440, 450]);
            await vi.advanceTimersByTimeAsync(0);
            expect((state._simulation.nodes() as D3Node[]).some(n => n.namespace === 'a' && n.userPinned)).toBe(true);
            root.querySelector<HTMLButtonElement>('[aria-label="Reset map"]')!.click();
            await page.updateComplete;
            expect(find('namespace:a')).toMatchObject({ x: -1000, y: 0 });
        });

        test.each([-1, 1])('reset keeps the edge pin clear with a neighbour on side %i', async side => {
            const { page, state } = await aggregate([makeFlow({ srcNs: 'a', dstNs: 'b' })]);
            const sim = state._simulation.stop().tick(300);
            sim.on('end')!.call(sim);
            await page.updateComplete;
            const root = page.shadowRoot!;
            const group = root.querySelector('.nodes [aria-label="Expand namespace a"]')!;
            const node = d3.select(group).datum() as D3Node;
            const peer = sim.nodes().find(n => n.id === 'namespace:b') as D3Node;
            Object.assign(node, { x: 0, y: 0 });
            const width = Number(group.querySelector('rect')!.getAttribute('width'));
            // The bodies clear each other, but one edge pin is inside the other body.
            const peerX = side * (width + 5);
            Object.assign(peer, { x: peerX, y: 0 });
            press(group, [240, 250]);
            await vi.advanceTimersByTimeAsync(0);
            root.querySelector<HTMLButtonElement>('[aria-label="Reset map"]')!.click();
            await page.updateComplete;
            expect(peer).toMatchObject({ x: peerX, y: 0 });
            expect(node.y).toBe(0);
            const [left, right] = side > 0 ? [node, peer] : [peer, node];
            expect(left.x! + edgePinPosition(left)[0] + 6).toBeLessThan(right.x! - width / 2);
        });

        test('reset restores the aggregate and its displaced neighbour, keeping new arrivals in place', async () => {
            const flow = makeFlow({ srcNs: 'a', dstNs: 'b' });
            const { page, state } = await aggregate([flow]);
            const settle = async () => {
                const simulation = state._simulation.stop().tick(300);
                simulation.on('end')!.call(simulation);
                await page.updateComplete;
            };
            await settle();
            const find = (id: string) => state._simulation.nodes().find(n => n.id === id)!;
            Object.assign(find('namespace:a'), { x: 0, y: 0 });
            Object.assign(find('namespace:b'), { x: 400, y: 0 });
            state._toggleNamespace('a');
            state._edgeLimit = 0;
            await page.updateComplete;
            const expanding = state._simulation.stop();
            const child = (expanding.nodes() as D3Node[]).find(n => n.namespace === 'a')!;
            child.x = 400; child.y = 0;
            expanding.on('end')!.call(expanding);
            expect([find('namespace:b').x, find('namespace:b').y]).not.toEqual([400, 0]);

            const store = new FlowStore();
            store.upsertBatch([flow, makeFlow({ srcNs: 'b', dstNs: 'c', srcIP: '10.9.1.1', dstIP: '10.9.1.2' })]);
            state._entries = store.getAll();
            await page.updateComplete;
            await settle();
            // The live arrival has found a position clear of both original namespaces.
            Object.assign(find('namespace:c'), { x: 850, y: 60 });
            const entries = state._entries;
            const root = page.shadowRoot!;
            const peer = root.querySelector<SVGGElement>('.nodes [aria-label="Expand namespace b"]')!;
            press(peer, [70, 80]);
            await vi.advanceTimersByTimeAsync(0);
            expect((d3.select(peer).datum() as D3Node).userPinned).toBe(true);
            const pin = root.querySelector('[aria-label="Keep edges highlighted for namespace b"]')!;
            pin.dispatchEvent(new MouseEvent('click', { bubbles: true }));
            expect(pin.getAttribute('aria-pressed')).toBe('true');
            const edge = root.querySelector('.edges path')!;
            edge.dispatchEvent(new MouseEvent('mouseenter'));
            edge.dispatchEvent(new MouseEvent('click'));
            await page.updateComplete;
            expect(root.querySelector('.edge-details')).not.toBeNull();

            const svg = root.querySelector<SVGSVGElement>('#graph-svg')!;
            root.querySelector<HTMLButtonElement>('[aria-label="Zoom in"]')!.click();
            const viewport = d3.zoomTransform(svg);
            root.querySelector<HTMLButtonElement>('[aria-label="Reset map"]')!.click();
            await page.updateComplete;
            expect(state._mapLayoutPending).toBe(false);
            expect(find('namespace:a')).toMatchObject({ x: 0, y: 0 });
            expect(find('namespace:b')).toMatchObject({ x: 400, y: 0 });
            expect(find('namespace:c')).toMatchObject({ x: 850, y: 60 });
            expect(state._entries).toBe(entries);
            expect(state._expandedNamespaces.size).toBe(0);
            expect(root.querySelector('.ns-hulls rect')).toBeNull();
            expect(root.querySelectorAll('.nodes .node')).toHaveLength(3);
            expect((state._simulation.nodes() as D3Node[]).every(n => !n.userPinned && n.fx == null && n.fy == null)).toBe(true);
            expect(root.querySelector('.edge-pin[aria-pressed="true"]')).toBeNull();
            expect(root.querySelector('.edge-details')).toBeNull();
            expect(state._hoveredEdgeKey).toBeNull();
            expect(root.querySelector<HTMLElement>('.map-tooltip')!.style.opacity).toBe('0');
            expect(root.querySelector<HTMLSelectElement>('#map-edge-limit')!.value).toBe('50');
            expect(d3.zoomTransform(svg)).toEqual(viewport);

            state._toggleNamespace('a');
            await page.updateComplete;
            await settle();
            expect(d3.zoomTransform(svg)).toEqual(viewport);
        });
    });

    describe('edge labels', () => {
        const setup = async () => {
            const { page, state } = await aggregate([
                makeFlow({ srcNs: 'a', dstNs: 'b' }),
                makeFlow({ srcNs: 'c', dstNs: 'd', srcIP: '10.0.1.1', dstIP: '10.0.1.2' }),
            ]);
            const simulation = state._simulation.stop().tick(300);
            simulation.on('end')!.call(simulation);
            await page.updateComplete;
            const root = page.shadowRoot!;
            const shown = () => Array.from(root.querySelectorAll('.edge-labels g'))
                .filter(label => label.getAttribute('display') !== 'none').length;
            const path = (source: string) => Array.from(root.querySelectorAll('.edges path'))
                .find(element => (d3.select(element).datum() as { edgeKey: string }).edgeKey.startsWith(source))!;
            return { page, state, root, shown, path };
        };

        test('are hidden until an edge is highlighted, hovered or selected', async () => {
            const { page, root, shown, path } = await setup();
            expect(root.querySelectorAll('.edge-labels g')).toHaveLength(2);
            expect(shown()).toBe(0);
            showEdgeLabels(root, 'namespace:a');
            expect(shown()).toBe(1);
            const node = Array.from(root.querySelectorAll('.nodes .node'))
                .find(element => (d3.select(element).datum() as D3Node).id === 'namespace:a')!;
            node.dispatchEvent(new MouseEvent('mouseleave'));
            expect(shown()).toBe(0);
            path('namespace:c').dispatchEvent(new MouseEvent('mouseenter'));
            expect(shown()).toBe(1);
            expect(path('namespace:c').getAttribute('opacity')).toBe('1');
            expect(path('namespace:a').getAttribute('opacity')).toBe('0.1');
            expect(Array.from(root.querySelectorAll('.nodes .node')).filter(n => n.getAttribute('opacity') === '1')
                .map(n => (d3.select(n).datum() as D3Node).id).sort()).toEqual(['namespace:c', 'namespace:d']);
            path('namespace:c').dispatchEvent(new MouseEvent('mouseleave'));
            expect(shown()).toBe(0);
            expect(Array.from(root.querySelectorAll('.nodes .node, .edges path')).every(n => n.getAttribute('opacity') === '1')).toBe(true);
            path('namespace:c').dispatchEvent(new MouseEvent('click'));
            await page.updateComplete;
            expect(shown()).toBe(1);
        });

        test('stay while a node is pinned and are placed on the edge when revealed', async () => {
            const { page, root, shown } = await setup();
            const pin = Array.from(root.querySelectorAll('.nodes .node'))
                .find(element => (d3.select(element).datum() as D3Node).id === 'namespace:a')!
                .querySelector('.edge-pin')!;
            pin.dispatchEvent(new MouseEvent('click', { bubbles: true }));
            await page.updateComplete;
            expect(shown()).toBe(1);
            const label = Array.from(root.querySelectorAll('.edge-labels g')).find(l => l.getAttribute('display') !== 'none')!;
            expect(label.getAttribute('transform')).toMatch(/^translate\(/);
        });

        test.each(['.edge-labels g', '.edge-hit-targets path'])('stay visible when the pointer moves from an edge onto %s', async selector => {
            const { root, shown, path } = await setup();
            const edge = path('namespace:c');
            const target = Array.from(root.querySelectorAll(selector))
                .find(l => (d3.select(l).datum() as { edgeKey: string }).edgeKey.startsWith('namespace:c'))!;
            edge.dispatchEvent(new MouseEvent('mouseenter'));
            edge.dispatchEvent(new MouseEvent('mouseleave', { relatedTarget: target }));
            expect(shown()).toBe(1);
            target.dispatchEvent(new MouseEvent('mouseleave'));
            expect(shown()).toBe(0);
        });

        test.each(['.edges path', '.edge-hit-targets path'])('preserves hover when moving between %s and the label background', async selector => {
            const { state, root, shown, path } = await setup();
            const key = 'namespace:c|namespace:d';
            const findEdge = (selector: string) => Array.from(root.querySelectorAll(selector))
                .find(element => (d3.select(element).datum() as { edgeKey: string }).edgeKey === key)!;
            const edge = findEdge(selector);
            const label = findEdge('.edge-labels g');
            const background = label.querySelector('rect')!;
            const tooltip = root.querySelector<HTMLElement>('.map-tooltip')!;
            const assertHovered = () => {
                expect(state._hoveredEdgeKey).toBe(key);
                expect(shown()).toBe(1);
                expect(tooltip.style.opacity).toBe('1');
                expect(path('namespace:c').getAttribute('opacity')).toBe('1');
                expect(path('namespace:a').getAttribute('opacity')).toBe('0.1');
            };
            edge.dispatchEvent(new MouseEvent('mouseenter'));
            // The browser reports the hit rect, not its parent group, as relatedTarget.
            edge.dispatchEvent(new MouseEvent('mouseleave', { relatedTarget: background }));
            assertHovered();
            label.dispatchEvent(new MouseEvent('mouseenter', { relatedTarget: edge }));
            label.dispatchEvent(new MouseEvent('mouseleave', { relatedTarget: edge }));
            assertHovered();
            edge.dispatchEvent(new MouseEvent('mouseenter', { relatedTarget: background }));
            edge.dispatchEvent(new MouseEvent('mouseleave', { relatedTarget: root.querySelector('.nodes .node') }));
            expect(state._hoveredEdgeKey).toBeNull();
            expect(shown()).toBe(0);
            expect(tooltip.style.opacity).toBe('0');
        });

        test.each(['path', 'label', 'hit target'])('clears hover and restores namespace backing when a hovered %s is replaced by busier traffic', async target => {
            const original = makeFlow({ srcNs: 'a', dstNs: 'b' });
            const peer = { srcNs: 'c', dstNs: 'd', srcIP: '10.0.1.1', dstIP: '10.0.1.2' };
            const competing = makeFlow(peer);
            const { page, state } = await aggregate([original, competing]);
            state._edgeLimit = 1;
            await page.updateComplete;
            const simulation = state._simulation.stop().tick(300);
            simulation.on('end')!.call(simulation);
            await page.updateComplete;
            const root = page.shadowRoot!;
            const edge = root.querySelector('.edges path')!;
            const label = root.querySelector('.edge-labels g')!;
            const hitTarget = root.querySelector('.edge-hit-targets path')!;
            const backings = () => Array.from(root.querySelectorAll('.namespace-backings rect'));
            const tooltip = root.querySelector<HTMLElement>('.map-tooltip')!;
            edge.dispatchEvent(new MouseEvent('mouseenter'));
            if (target !== 'path') {
                const next = target === 'label' ? label : hitTarget;
                edge.dispatchEvent(new MouseEvent('mouseleave', { relatedTarget: next }));
                next.dispatchEvent(new MouseEvent('mouseenter'));
            }
            expect(state._hoveredEdgeKey).toBe('namespace:a|namespace:b');
            expect(backings().filter(b => b.getAttribute('opacity') === '0')).toHaveLength(2);
            expect(tooltip.style.opacity).toBe('1');

            // The edge remains in the graph, but streaming counts replace it in the visible top
            // edges. Removing its DOM elements does not send a mouseleave event.
            const store = new FlowStore();
            store.upsertBatch([original, competing,
                makeFlow({ ...peer, dstPort: 81 }), makeFlow({ ...peer, dstPort: 82 })]);
            state._entries = store.getAll();
            await page.updateComplete;
            expect(edge.isConnected).toBe(false);
            expect(label.isConnected).toBe(false);
            expect(hitTarget.isConnected).toBe(false);
            expect(state._graphRef.edges.some(e => e.source === 'namespace:a' && e.target === 'namespace:b')).toBe(true);
            expect(state._hoveredEdgeKey).toBeNull();
            expect(backings().every(b => b.getAttribute('opacity') === '1')).toBe(true);
            expect(Array.from(root.querySelectorAll('.nodes .node, .edges path')).every(n => n.getAttribute('opacity') === '1')).toBe(true);
            expect(tooltip.style.opacity).toBe('0');

            // Bringing the old edge back must not revive its stale hover or port label.
            state._edgeLimit = 0;
            await page.updateComplete;
            expect(root.querySelectorAll('.edges path')).toHaveLength(2);
            expect(Array.from(root.querySelectorAll('.edge-labels g')).every(g => g.getAttribute('display') === 'none')).toBe(true);
            expect(backings().every(b => b.getAttribute('opacity') === '1')).toBe(true);
        });
    });

    test('hovering a node does not build the graph again and restyles the drawn edges', async () => {
        const { page, state } = await aggregate([makeFlow({ srcNs: 'a', dstNs: 'b' })]);
        state._simulation.stop();
        const root = page.shadowRoot!;
        const graph = state._graphRef;
        const nodes = Array.from(root.querySelectorAll<SVGGElement>('.nodes .node'));
        const path = root.querySelector('.edges path')!;
        nodes[0].dispatchEvent(new MouseEvent('mouseenter'));
        expect(state._graphRef).toBe(graph);
        expect(path.getAttribute('opacity')).toBe('1');
        nodes[0].dispatchEvent(new MouseEvent('mouseleave'));
        expect(state._graphRef).toBe(graph);
        expect(root.querySelector('.edges path')).toBe(path);
    });

    test('the expand tooltip follows the namespace body and clears when the pointer crosses onto the pin', async () => {
        const { page } = await aggregate([makeFlow({ srcNs: 'a', dstNs: 'b' })]);
        const root = page.shadowRoot!;
        const body = root.querySelector('[aria-label="Expand namespace a"]')!;
        const node = body.parentElement!;
        const pin = node.querySelector('.edge-pin')!;
        const tooltip = root.querySelector<HTMLElement>('#graph-tooltip')!;
        node.dispatchEvent(new MouseEvent('mouseenter'));
        pin.dispatchEvent(new MouseEvent('mouseenter'));
        expect(tooltip.style.opacity).not.toBe('1');
        body.dispatchEvent(new MouseEvent('mouseenter', { relatedTarget: pin }));
        expect(tooltip.textContent).toContain('Click to expand.');
        expect(tooltip.style.opacity).toBe('1');
        body.dispatchEvent(new MouseEvent('mouseleave', { relatedTarget: pin }));
        pin.dispatchEvent(new MouseEvent('mouseenter', { relatedTarget: body }));
        expect(tooltip.style.opacity).toBe('0');
        pin.dispatchEvent(new MouseEvent('click', { bubbles: true }));
        expect(pin.getAttribute('aria-pressed')).toBe('true');
        expect(body.isConnected).toBe(true);
    });

    test('scoped pins identify workloads with the same name in different namespaces', async () => {
        const { page } = await aggregate([makeFlow({
            srcNs: 'a', dstNs: 'b', srcPod: 'api-abc12', dstPod: 'api-xyz34',
        })], false);
        const pins = Array.from(page.shadowRoot!.querySelectorAll('.edge-pin'), pin => pin.getAttribute('aria-label'));
        expect(pins.sort()).toEqual(['Keep edges highlighted for a/api', 'Keep edges highlighted for b/api']);
    });

    test('folded and withheld namespaces expose separate keyboard-accessible edge pins', async () => {
        const { page, state } = await aggregate([
            makeFlow({ srcNs: 'a', dstNs: 'b' }),
            makeFlow({ srcNs: 'a', dstNs: 'b', dstPort: 81, dstDisclosure: EndpointDisclosure.Flow }),
        ]);
        const root = page.shadowRoot!;
        for (const [name, role] of [['namespace b', 'button'], ['undisclosed workloads in namespace b', 'img']]) {
            const pin = root.querySelector<SVGTextElement>(`[aria-label="Keep edges highlighted for ${name}"]`)!;
            const node = pin.closest('.node')!;
            expect(node.querySelector(`.node-body[role="${role}"]`)).not.toBeNull();
            expect(pin.parentElement!.closest('[role="button"], [role="img"]')).toBeNull();
            pin.focus();
            expect(root.activeElement).toBe(pin);
            for (const [key, pressed] of [['Enter', 'true'], [' ', 'false']]) {
                pin.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true }));
                expect(pin.getAttribute('aria-pressed')).toBe(pressed);
                expect(state._expandedNamespaces.size).toBe(0);
            }
        }
    });

    test('keeps redacted and external endpoints distinct and never expands them', async () => {
        const { page, state } = await aggregate([
            makeFlow({ srcNs: 'a', dstNs: 'b' }),
            makeFlow({ srcNs: 'a', dstNs: 'b', dstPort: 81, dstDisclosure: EndpointDisclosure.Flow }),
            makeFlow({ srcNs: 'a', dstNs: '', dstPort: 82, dstDisclosure: EndpointDisclosure.Flow }),
            makeFlow({ srcNs: 'a', dstNs: '', dstPort: 83, flowType: FlowType.ToExternal }),
        ]);
        expect(state._graphRef.nodes.map(n => n.kind).sort()).toEqual([
            'external', 'namespace', 'namespace', 'undisclosed', 'undisclosedNamespace',
        ]);
        const redacted = Array.from(page.shadowRoot!.querySelectorAll<SVGGElement>('.nodes .node'))
            .find(el => (d3.select(el).datum() as { kind: string }).kind === 'undisclosedNamespace')!;
        redacted.dispatchEvent(new MouseEvent('click', { bubbles: true }));
        await page.updateComplete;
        expect(state._expandedNamespaces.size).toBe(0);
        state._toggleNamespace('b');
        await page.updateComplete;
        expect(state._graphRef.nodes.some(n => n.id === 'undisclosed-namespace:b')).toBe(true);
        expect(state._graphRef.nodes.some(n => n.id === 'b/server')).toBe(true);
    });

    test.each([
        { disclosure: EndpointDisclosure.Full, target: 'b' },
        { disclosure: EndpointDisclosure.Flow, target: 'b (2 addresses)' },
    ])('shows the endpoint label $target in the edge tooltip and details', async ({ disclosure, target }) => {
        const { page } = await aggregate([
            makeFlow({ srcNs: 'a', dstNs: 'a', dstPort: 81, ingressAction: NetworkPolicyRuleAction.Drop }),
            makeFlow({ srcNs: 'a', dstNs: 'b', dstDisclosure: disclosure }),
            makeFlow({ srcNs: 'a', dstNs: 'b', dstDisclosure: disclosure, dstIP: '10.0.0.3' }),
        ]);
        const root = page.shadowRoot!;
        const edge = root.querySelector('.edges path')!;
        edge.dispatchEvent(new MouseEvent('mouseenter'));
        expect(root.querySelector('#graph-tooltip > div')!.textContent).toBe(`a → ${target}`);
        edge.dispatchEvent(new MouseEvent('click'));
        await page.updateComplete;
        expect(root.querySelector('.edge-details-title')!.textContent).toBe(`a → ${target}`);
        expect(root.querySelector('.edge-details-rows')!.textContent).toContain('2 connections');
        expect(root.querySelectorAll('.edge-connection')).toHaveLength(2);
        expect(root.querySelector('.nodes')!.textContent).toContain('1 internal · 1 denied');
    });

    test('publishes endpoint kinds and clears folded selection on expansion', async () => {
        const { page, state } = await aggregate([makeFlow({ srcNs: 'a', dstNs: 'b' })]);
        const selected = vi.fn();
        page.addEventListener('antrea-edge-selected', selected);
        page.shadowRoot!.querySelector('.edges path')!.dispatchEvent(new MouseEvent('click'));
        await page.updateComplete;
        expect(selected.mock.calls.at(-1)![0].detail).toMatchObject({ source: 'namespace:a', sourceKind: 'namespace', targetKind: 'namespace' });
        state._toggleNamespace('a');
        await page.updateComplete;
        await page.updateComplete;
        expect(state._selectedEdgeKey).toBeNull();
        expect(selected.mock.calls.at(-1)![0].detail).toBeNull();
    });

    test('hover and clicking retain the same visualization without changing positions or layout links', async () => {
        const { page, state } = await aggregate([
            makeFlow({ srcNs: 'a', dstNs: 'b' }),
            makeFlow({ srcNs: 'a', dstNs: 'c', dstPort: 81 }),
            makeFlow({ srcNs: 'c', dstNs: 'd', dstPort: 82 }),
        ]);
        state._edgeLimit = 1;
        state._busiestEdges.clear();
        await page.updateComplete;
        const simulation = state._simulation.stop().alpha(0);
        const restart = vi.spyOn(simulation, 'restart');
        const positions = simulation.nodes().map(n => [n.x, n.y]);
        const root = page.shadowRoot!;
        const count = async () => {
            await page.updateComplete;
            return root.querySelector('.map-edge-count')!.textContent;
        };
        expect(await count()).toBe('1 of 3 edges');
        expect(root.querySelector('.map-edge-count')!.closest('[role="status"], [aria-live="polite"], [aria-live="assertive"]')).toBeNull();
        const limit = root.querySelector<HTMLSelectElement>('#map-edge-limit')!;
        expect(limit.classList.contains('field-select')).toBe(true);
        expect(limit.labels![0].textContent).toBe('Busiest edges (connections)');
        expect(limit.hasAttribute('aria-label')).toBe(false);
        const a = Array.from(root.querySelectorAll<SVGGElement>('.nodes .node'))
            .find(el => (d3.select(el).datum() as PositionedNode).id === 'namespace:a')!;
        expect(root.querySelectorAll('.edges path')).toHaveLength(1);
        a.dispatchEvent(new MouseEvent('mouseenter'));
        expect(root.querySelectorAll('.edges path')).toHaveLength(2);
        expect(await count()).toBe('2 of 3 edges');
        const visualization = () => ['.nodes .node', '.edges path', '.edge-labels g'].map(selector =>
            Array.from(root.querySelectorAll(selector)).map(el => {
                const datum = d3.select(el).datum() as { id?: string; edgeKey?: string };
                return [datum.id ?? datum.edgeKey, el.getAttribute('opacity')];
            }));
        const hovered = visualization();
        a.querySelector('.edge-pin')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
        a.dispatchEvent(new MouseEvent('mouseleave'));
        expect(visualization()).toEqual(hovered);
        expect(root.querySelectorAll('.edges path')).toHaveLength(2);
        expect(await count()).toBe('2 of 3 edges');
        expect((simulation.force('link') as d3.ForceLink<PositionedNode, d3.SimulationLinkDatum<PositionedNode>>).links()).toHaveLength(1);
        expect(simulation.nodes().map(n => [n.x, n.y])).toEqual(positions);
        expect(restart).not.toHaveBeenCalled();
        a.querySelector('.edge-pin')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
        expect(root.querySelectorAll('.edges path')).toHaveLength(1);
        expect(await count()).toBe('1 of 3 edges');
        state._edgeLimit = 0;
        await page.updateComplete;
        expect(await count()).toBe('3 of 3 edges');
        state._edgeLimit = 1;
        await page.updateComplete;
        expect(await count()).toBe('1 of 3 edges');
        state._toggleNamespace('a');
        await page.updateComplete;
        expect(root.querySelectorAll('.edges path')).toHaveLength(2);
    });

    test.each(['individual', 'all'])('reranks tied edges on expansion and %s namespace collapse', async mode => {
        const { page, state } = await aggregate([
            makeFlow({ srcNs: 'a', dstNs: 'b' }),
            makeFlow({ srcNs: 'a', dstNs: 'b', srcPod: 'second-abc12', dstPort: 81 }),
            makeFlow({ srcNs: 'c', dstNs: 'd', dstPort: 82 }),
            makeFlow({ srcNs: 'c', dstNs: 'd', dstPort: 83 }),
        ]);
        state._edgeLimit = 1;
        const settle = async () => {
            await page.updateComplete;
            state._simulation.stop().tick(300);
            state._simulation.on('end')!.call(state._simulation);
            await page.updateComplete;
        };
        await settle();
        const root = page.shadowRoot!;
        const drawn = () => Array.from(root.querySelectorAll('.edges path'),
            path => (d3.select(path).datum() as { edgeKey: string }).edgeKey).sort();
        expect(drawn()).toEqual(['namespace:a|namespace:b']);

        // Splitting a's two connections across workloads leaves c → d as the busiest edge.
        root.querySelector('[aria-label="Expand namespace a"]')!.dispatchEvent(new MouseEvent('click'));
        await settle();
        expect(drawn()).toEqual(['a/client|namespace:b', 'a/second|namespace:b', 'namespace:c|namespace:d']);
        if (mode === 'all') root.querySelector<HTMLButtonElement>('[aria-label="Collapse all namespaces"]')!.click();
        else root.querySelector('.ns-hulls text')!.dispatchEvent(new MouseEvent('click'));
        await settle();
        expect(drawn()).toEqual(['namespace:a|namespace:b']);

        // Expanding c keeps its two connections together. Its workload now wins the stable tie.
        root.querySelector('[aria-label="Expand namespace c"]')!.dispatchEvent(new MouseEvent('click'));
        await settle();
        expect(drawn()).toEqual(['c/client|namespace:d']);
    });

    test('retained highlights combine with hover and preserve busy edges, selection, layout and zoom', async () => {
        const { page, state } = await aggregate([
            makeFlow({ srcNs: 'a', dstNs: 'b' }),
            makeFlow({ srcNs: 'a', dstNs: 'c', dstPort: 81 }),
            makeFlow({ srcNs: 'c', dstNs: 'd', dstPort: 82 }),
            makeFlow({ srcNs: 'c', dstNs: 'd', dstPort: 82, srcIP: '10.0.0.3' }),
            makeFlow({ srcNs: 'c', dstNs: 'd', dstPort: 82, srcIP: '10.0.0.4' }),
            makeFlow({ srcNs: 'd', dstNs: 'e', dstPort: 83 }),
            makeFlow({ srcNs: 'e', dstNs: 'f', dstPort: 84 }),
        ]);
        state._edgeLimit = 1;
        state._busiestEdges.clear();
        await page.updateComplete;
        const simulation = state._simulation.stop().alpha(0);
        simulation.on('end')!.call(simulation);
        const positions = simulation.nodes().map(n => [n.x, n.y]);
        const links = () => (simulation.force('link') as d3.ForceLink<PositionedNode, d3.SimulationLinkDatum<PositionedNode> & { edgeKey: string }>).links().map(link => link.edgeKey);
        const originalLinks = links();
        const restart = vi.spyOn(simulation, 'restart');
        const root = page.shadowRoot!;
        const drawn = () => Array.from(root.querySelectorAll('.edges path'))
            .map(path => (d3.select(path).datum() as { edgeKey: string }).edgeKey).sort();
        const node = (namespace: string) => Array.from(root.querySelectorAll('.nodes .node'))
            .find(element => (d3.select(element).datum() as PositionedNode).id === `namespace:${namespace}`)!;
        const mark = async (namespace: string) => {
            node(namespace).querySelector('.edge-pin')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
            await page.updateComplete;
        };
        const svg = root.querySelector<SVGSVGElement>('#graph-svg')!;
        const transform = d3.zoomIdentity.translate(42, 21).scale(1.5);
        (svg as SVGSVGElement & { __zoom: d3.ZoomTransform }).__zoom = transform;
        const selected = vi.fn();
        page.addEventListener('antrea-edge-selected', selected);
        expect(drawn()).toEqual(['namespace:c|namespace:d']);
        state._selectedEdgeKey = 'namespace:d|namespace:e';
        await page.updateComplete;
        expect(drawn()).toEqual(['namespace:c|namespace:d', 'namespace:d|namespace:e']);

        await mark('a');
        const pinnedEdges = ['namespace:a|namespace:b', 'namespace:a|namespace:c', 'namespace:c|namespace:d', 'namespace:d|namespace:e'];
        expect(drawn()).toEqual(pinnedEdges);
        expect(state._selectedEdgeKey).toBe('namespace:d|namespace:e');
        expect(selected.mock.calls.at(-1)![0].detail).toMatchObject({ source: 'namespace:d', target: 'namespace:e' });
        const opacity = (key: string) => Array.from(root.querySelectorAll('.edges path'))
            .find(path => (d3.select(path).datum() as { edgeKey: string }).edgeKey === key)!.getAttribute('opacity');
        expect(opacity('namespace:c|namespace:d')).toBe('0.1');
        expect(opacity('namespace:d|namespace:e')).toBe('0.1');
        expect(node('d').getAttribute('opacity')).toBe('0.2');
        expect(root.querySelector('.map-toolbar')!.textContent).toContain('Click ↔ to keep the highlight');
        node('e').dispatchEvent(new MouseEvent('mouseenter'));
        expect(drawn()).toEqual([...pinnedEdges, 'namespace:e|namespace:f']);
        expect(opacity('namespace:a|namespace:b')).toBe('1');
        expect(opacity('namespace:d|namespace:e')).toBe('1');
        node('e').dispatchEvent(new MouseEvent('mouseleave'));
        expect(drawn()).toEqual(pinnedEdges);
        expect(opacity('namespace:d|namespace:e')).toBe('0.1');

        await mark('d');
        expect(drawn()).toEqual(['namespace:a|namespace:b', 'namespace:a|namespace:c', 'namespace:c|namespace:d', 'namespace:d|namespace:e']);
        expect(opacity('namespace:c|namespace:d')).toBe('1');
        await mark('a');
        expect(drawn()).toEqual(['namespace:c|namespace:d', 'namespace:d|namespace:e']);
        await mark('d');
        expect(drawn()).toEqual(['namespace:c|namespace:d', 'namespace:d|namespace:e']);
        expect(opacity('namespace:c|namespace:d')).toBe('1');
        expect(state._selectedEdgeKey).toBe('namespace:d|namespace:e');
        expect(links()).toEqual(originalLinks);
        expect(simulation.nodes().map(n => [n.x, n.y])).toEqual(positions);
        expect(d3.zoomTransform(svg)).toEqual(transform);
        expect(root.querySelector('.map-content')!.getAttribute('transform')).toBe(transform.toString());
        expect(restart).not.toHaveBeenCalled();
        restart.mockRestore();
    });

    test('retained highlights preserve expanded namespace edges and disappear with their node', async () => {
        const { page, state } = await aggregate([
            makeFlow({ srcNs: 'a', dstNs: 'b' }),
            makeFlow({ srcNs: 'b', dstNs: 'c', dstPort: 81 }),
            makeFlow({ srcNs: 'c', dstNs: 'd', dstPort: 82 }),
        ]);
        state._edgeLimit = 1;
        state._busiestEdges.clear();
        state._simulation.stop().alpha(0);
        state._simulation.on('end')!.call(state._simulation);
        state._toggleNamespace('b');
        await page.updateComplete;
        const root = page.shadowRoot!;
        const mark = Array.from(root.querySelectorAll('.nodes .node'))
            .find(element => (d3.select(element).datum() as PositionedNode).id === 'namespace:a')!.querySelector('.edge-pin')!;
        expect(root.querySelectorAll('.edges path')).toHaveLength(2);
        mark.dispatchEvent(new MouseEvent('click', { bubbles: true }));
        await page.updateComplete;
        const paths = root.querySelectorAll('.edges path');
        expect(paths).toHaveLength(2);
        expect((d3.select(paths[0]).datum() as { edgeKey: string }).edgeKey).toBe('namespace:a|b/server');
        expect(paths[0].getAttribute('opacity')).toBe('1');
        expect(paths[1].getAttribute('opacity')).toBe('0.1');

        // Expanding the marked aggregate removes that node and its retained highlight.
        state._toggleNamespace('a');
        await page.updateComplete;
        await page.updateComplete;
        expect(root.querySelectorAll('.edges path')).toHaveLength(2);
        expect(Array.from(root.querySelectorAll('.edges path, .nodes .node')).every(el => el.getAttribute('opacity') === '1')).toBe(true);
    });

    test('edge guidance explains persistent highlights and the current numeric limit', async () => {
        const { page, state } = await aggregate([makeFlow({ srcNs: 'a', dstNs: 'b' })]);
        state._simulation.stop().alpha(0);
        state._simulation.on('end')!.call(state._simulation);
        await page.updateComplete;
        const root = page.shadowRoot!;
        const toolbar = () => root.querySelector('.map-toolbar')!.textContent!;
        const mark = root.querySelector('.edge-pin')!;
        expect(toolbar()).toContain('Expanded namespaces, highlighted nodes, and selected edges can show more than 50 edges.');
        for (let i = 0; i < 2; i++) {
            mark.dispatchEvent(new MouseEvent('click', { bubbles: true }));
            await page.updateComplete;
            expect(root.querySelector('.map-edge-count')!.textContent).toBe('1 of 1 edges');
            expect(mark.getAttribute('aria-pressed')).toBe(i === 0 ? 'true' : 'false');
            expect(toolbar()).toContain('Click ↔ to keep the highlight; click again to release it');
        }
        state._clusterWide = false;
        await page.updateComplete;
        expect(toolbar()).toContain('Highlighted nodes and selected edges can show more than 50 edges.');
        expect(toolbar()).not.toContain('Expanded namespaces');
        state._edgeLimit = 0;
        await page.updateComplete;
        expect(toolbar()).not.toContain('can show more than');
    });

    test.each(['mouse', 'touch'])('%s activation of an edge marker does not pin the node position', async input => {
        vi.stubGlobal('navigator', { maxTouchPoints: 1 });
        const { page, state } = await aggregate([makeFlow({ srcNs: 'a', dstNs: 'b' })]);
        const simulation = state._simulation.stop().alpha(0);
        simulation.on('end')!.call(simulation);
        await page.updateComplete;
        const marker = page.shadowRoot!.querySelector('.edge-pin')!;
        const node = d3.select(marker).datum() as D3Node;
        if (input === 'mouse') {
            const down = new MouseEvent('mousedown', { bubbles: true, clientX: 40, clientY: 50 });
            Object.defineProperty(down, 'view', { value: window });
            marker.dispatchEvent(down);
        } else {
            const contact = { identifier: 1, target: marker, clientX: 40, clientY: 50 } as Touch;
            marker.dispatchEvent(new TouchEvent('touchstart', {
                bubbles: true, cancelable: true,
                touches: [contact], targetTouches: [contact], changedTouches: [contact],
            }));
        }
        marker.dispatchEvent(new MouseEvent('click', { bubbles: true }));
        await page.updateComplete;
        expect(marker.getAttribute('aria-pressed')).toBe('true');
        expect(node.userPinned).not.toBe(true);
        expect(node.fx == null && node.fy == null).toBe(true);
    });
});


test('single-namespace maps keep workloads while limiting edges and retaining selection', async () => {
    const page = await mount(async () => sseResponse([]));
    await vi.advanceTimersByTimeAsync(0);
    page.viewMode = 'map';
    await page.updateComplete;
    const state = page as unknown as {
        _entries: FlowEntry[]; _edgeLimit: number; _selectedEdgeKey: string | null;
        _graphRef: { nodes: { kind: string }[] };
    };
    const store = new FlowStore();
    store.upsertBatch([
        makeFlow({ dstPod: 'aaa-abc12' }), makeFlow({ dstPod: 'zzz-abc12', dstPort: 81 }),
    ]);
    state._entries = store.getAll();
    state._edgeLimit = 1;
    await page.updateComplete;
    expect(state._graphRef.nodes.every(n => n.kind === 'workload')).toBe(true);
    expect(page.shadowRoot!.querySelectorAll('.edges path')).toHaveLength(1);
    state._selectedEdgeKey = 'default/client|default/zzz';
    await page.updateComplete;
    expect(page.shadowRoot!.querySelectorAll('.edges path')).toHaveLength(2);
    state._selectedEdgeKey = null;
    await page.updateComplete;
    expect(page.shadowRoot!.querySelectorAll('.edges path')).toHaveLength(1);
});
