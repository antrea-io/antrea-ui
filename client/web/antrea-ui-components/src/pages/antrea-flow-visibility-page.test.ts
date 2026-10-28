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

import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';
import './antrea-flow-visibility-page';
import { SCOPE_CHANGE_EVENT, SCOPE_CHOSEN_EVENT } from '../lib/namespace-scope';
import type { ScopeChangeDetail, ScopeChosenDetail } from '../lib/namespace-scope';
import type { AntreaFlowVisibilityPage } from './antrea-flow-visibility-page';
import type { FlowStreamFilter } from '../lib/flow-stream';
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

/** Ticks a peer-namespace menu option by its label. The menu's first entry is the
 * "Unidentified peers" pseudo-option, so an index would be picking that one. */
function toggleOption(page: AntreaFlowVisibilityPage, label: string): void {
    const option = Array.from(page.shadowRoot!.querySelectorAll('.multiselect-option'))
        .find(el => el.textContent!.trim() === label);
    if (!option) throw new Error(`no filter option "${label}" in [${peerNamespaceOptions(page).join(', ')}]`);
    option.querySelector<HTMLInputElement>('input[type="checkbox"]')!
        .dispatchEvent(new Event('change', { bubbles: true }));
}

function peerNamespaceOptions(page: AntreaFlowVisibilityPage): string[] {
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
    };
}

const FLOWS_WATCH_RULE = { verbs: ['watch'], apiGroups: ['observability.antrea.io'], resources: ['flows'] };

/** GET /api/v1/access-summary: the cluster-scoped one, which holds the grant for a cluster-wide
 * caller and names the namespaces the page offers. */
function accessSummaryResponse(f: FlowAccessFixture): Response {
    return jsonResponse({
        username: 'alice', groups: [], clusterAdmin: false,
        namespaces: f.lister ? ['*'] : f.namespaces,
        rules: {
            resourceRules: f.clusterWide === 'allowed' ? [FLOWS_WATCH_RULE] : [],
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
    opts: { search?: string; flowAccess?: () => FlowAccessFixture } = {},
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

    // No scope change can turn the integration on.
    test('the integration-disabled banner stays', async () => {
        const fetchMock = vi.fn(async (url: string) => url.includes('observedNamespace=ns-a')
            ? new Response(JSON.stringify({ code: 'integration_disabled', message: 'off' }), { status: 501 })
            : sseResponse([]));
        const page = await mount(fetchMock, {
            search: '/?namespace=ns-a',
            flowAccess: () => flowAccessFixture({ namespaces: ['ns-a', 'ns-b'] }),
        });
        await vi.advanceTimersByTimeAsync(0);
        await page.updateComplete;
        const before = page.shadowRoot!.querySelector('antrea-alert[status="danger"]')?.textContent;

        await chooseScope(page, 'ns-b');
        await vi.advanceTimersByTimeAsync(0);
        await page.updateComplete;

        expect(before).toBeTruthy();
        expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')?.textContent).toBe(before);
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

    // A pending pick that was never applied would otherwise survive the switch to cluster scope,
    // where the menu no longer offers it, and Apply would empty the list.
    test('a pending Unidentified peers pick is dropped when the scope becomes All Namespaces', async () => {
        const page = await mount(async () => sseResponse([]), {
            search: '/?namespace=ns-a',
            flowAccess: () => flowAccessFixture({ clusterWide: 'allowed', namespaces: ['ns-a'] }),
        });
        page.shadowRoot!.querySelector<HTMLButtonElement>('.multiselect-btn')!.click();
        await page.updateComplete;
        toggleOption(page, 'Unidentified peers');
        await page.updateComplete;

        await chooseScope(page, '__cluster_wide__');

        const priv = page as unknown as { _pendingNs: string[]; _unidentifiedPeersOnly: boolean };
        expect(priv._pendingNs).not.toContain('Unidentified peers');
        expect(priv._unidentifiedPeersOnly).toBe(false);
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
        // Observes a namespace the fixtures' flows are not in, so that their own namespace shows
        // up as a peer: the observed namespace is excluded from its own peer list.
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
            makeFlow({ srcPod: 'm-withheld', srcIP: '10.0.0.2', dstDisclosure: EndpointDisclosure.Flow }),
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

describe('AntreaFlowVisibilityPage — flow visibility disabled server-side', () => {
    // The backend answers with 501 (a static per-deployment config choice, not a transient
    // failure) when Flow Aggregator integration is off. This is terminal, like a 401: no retry.
    test('a stream 501 shows the disabled message and does not retry the stream', async () => {
        const fetchMock = vi.fn(async () => sseResponse([], 501));
        const page = await mount(fetchMock);
        await vi.advanceTimersByTimeAsync(0);

        expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')?.textContent)
            .toContain('Flow visibility is disabled');

        expect(streamCalls(fetchMock)).toHaveLength(1);
        await vi.advanceTimersByTimeAsync(60_000);
        expect(streamCalls(fetchMock)).toHaveLength(1);
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
            return sseResponse([], status);
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

// The 403 twin of the 501 case above: since nothing on the frontend gates access to this page
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

// The namespace multi-select is a peer filter now, not a second scope control: it selects flows
// by their far end, which upstream explicitly supports naming an out-of-scope namespace for.
describe('AntreaFlowVisibilityPage — peer namespace filter', () => {
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
    test('offers every namespace seen in the current flows', async () => {
        const page = await mountWithPeers();
        expect(peerNamespaceOptions(page)).toEqual(['Unidentified peers', 'default', 'kube-system']);
        expect(page.shadowRoot!.querySelector('.multiselect-hint')!.textContent)
            .toContain('seen in current flows');
    });

    test('the observed namespace is not offered as its own peer', async () => {
        const page = await mountWithPeers('/?namespace=default');
        expect(peerNamespaceOptions(page)).not.toContain('default');
    });

    test('a selection stays in the menu once its traffic goes quiet', async () => {
        const page = await mountWithPeers();
        toggleOption(page, 'kube-system');
        await page.updateComplete;

        (page as unknown as { _entries: unknown[] })._entries = [];
        await page.updateComplete;
        expect(peerNamespaceOptions(page)).toContain('kube-system');
    });

    test('it is labelled Peer namespace, in both views', async () => {
        const page = await mountWithPeers();
        const label = () => Array.from(page.shadowRoot!.querySelectorAll('.multiselect-label'))
            .map(el => el.textContent!.trim());
        expect(label()).toContain('Peer namespace');

        page.viewMode = 'map';
        await page.updateComplete;
        expect(label()).toContain('Peer namespace');
    });
});

// The applied peer filter is what the stream asks for, and the control shows what is selected: when
// the scope moves onto a namespace the filter names, both have to drop it, or the stream goes on
// asking for something the control no longer shows.
describe('AntreaFlowVisibilityPage — a scope change and the applied peer filter', () => {
    test('drops the new scope from the applied peer namespaces', async () => {
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
        expect(params.get('namespaces')).toBe('ns-y');
    });

    test('drops the filter altogether when the new scope was the only peer namespace', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        const page = await mount(fetchMock, {
            search: '/?namespace=default',
            flowAccess: () => flowAccessFixture({
                namespaces: ['default', 'ns-x'],
            }),
        });
        (page as unknown as { _applyFilter(filter: FlowStreamFilter): void })
            ._applyFilter({ namespaces: ['ns-x'] });
        await page.updateComplete;
        await vi.advanceTimersByTimeAsync(0);

        await chooseScope(page, 'ns-x');
        await vi.advanceTimersByTimeAsync(0);

        const calls = streamCalls(fetchMock);
        const params = new URL(calls[calls.length - 1][0] as string, 'http://example.test').searchParams;
        expect(params.has('namespaces')).toBe(false);
    });
});

// The one filter the Flow Aggregator cannot apply: it selects records by what was withheld from
// them, and upstream authorizes each batch before applying filters, so a server-side filter only
// ever matches fields that survived redaction.
describe('AntreaFlowVisibilityPage — the Unidentified peers pseudo-filter', () => {
    async function mountMixed(): Promise<{ page: AntreaFlowVisibilityPage; fetchMock: ReturnType<typeof vi.fn> }> {
        const flows = [
            makeFlow({ srcPod: 'a-abc12', srcIP: '10.0.0.1' }),
            makeFlow({
                srcPod: 'b-xyz34', srcIP: '10.0.0.2', dstIP: '10.0.0.3',
                dstNs: 'ns-c', dstPod: '', dstDisclosure: EndpointDisclosure.Flow,
            }),
        ];
        const fetchMock = vi.fn(async () => sseResponse([flowEventChunk(flows)]));
        const page = await mount(fetchMock, { search: '/?namespace=default' });
        for (let i = 0; i < 5 && page.shadowRoot!.querySelectorAll('tbody tr').length < 2; i++) {
            await vi.advanceTimersByTimeAsync(1000);
            await page.updateComplete;
        }
        return { page, fetchMock };
    }

    async function applyUnidentifiedPeers(page: AntreaFlowVisibilityPage): Promise<void> {
        page.shadowRoot!.querySelector<HTMLButtonElement>('.multiselect-btn')!.click();
        await page.updateComplete;
        toggleOption(page, 'Unidentified peers');
        await page.updateComplete;
        page.shadowRoot!.querySelector<HTMLElement>('.filter-actions antrea-button')!.click();
        await page.updateComplete;
        await vi.advanceTimersByTimeAsync(0);
    }

    test('narrows the list to flows with a withheld endpoint', async () => {
        const { page } = await mountMixed();
        expect(page.shadowRoot!.querySelectorAll('tbody tr')).toHaveLength(2);

        await applyUnidentifiedPeers(page);

        const rows = page.shadowRoot!.querySelectorAll('tbody tr');
        expect(rows).toHaveLength(1);
        expect(rows[0].textContent).toContain('ns-c');
    });

    // Purely local: it is not a namespace, so sending it would either be rejected or match
    // nothing, and it needs no reconnect.
    test('is not sent to the backend, and does not reopen the stream', async () => {
        const { page, fetchMock } = await mountMixed();
        expect(streamCalls(fetchMock)).toHaveLength(1);

        await applyUnidentifiedPeers(page);

        expect(streamCalls(fetchMock)).toHaveLength(1);
        expect(streamCalls(fetchMock)[0][0]).not.toContain('Unidentified');
    });

    // The Flow Aggregator gives an endpoint with no namespace the lowest tier, outside the
    // cluster included, but nothing is withheld from one: it is not a peer that went unidentified.
    test('does not select flows to or from outside the cluster', async () => {
        const flows = [
            makeFlow({
                srcPod: 'a-abc12', srcIP: '10.0.0.1', dstIP: '8.8.8.8', dstNs: '', dstPod: '',
                dstDisclosure: EndpointDisclosure.Flow, flowType: FlowType.ToExternal,
            }),
            makeFlow({
                srcPod: 'e-abc12', srcIP: '1.1.1.1', dstIP: '10.0.0.4', srcNs: '', srcDisclosure: EndpointDisclosure.Flow,
                flowType: FlowType.FromExternal,
            }),
            makeFlow({
                srcPod: 'b-xyz34', srcIP: '10.0.0.2', dstIP: '10.0.0.3',
                dstNs: 'ns-c', dstPod: '', dstDisclosure: EndpointDisclosure.Flow,
            }),
        ];
        const page = await mount(async () => sseResponse([flowEventChunk(flows)]), {
            search: '/?namespace=default',
        });
        for (let i = 0; i < 5 && page.shadowRoot!.querySelectorAll('tbody tr').length < 3; i++) {
            await vi.advanceTimersByTimeAsync(1000);
            await page.updateComplete;
        }
        await applyUnidentifiedPeers(page);

        const rows = page.shadowRoot!.querySelectorAll('tbody tr');
        expect(rows).toHaveLength(1);
        expect(rows[0].textContent).toContain('ns-c');
    });

    // Every other option in the menu widens the choice. This one is applied on what the
    // namespaces left, so the two together would narrow it: they are not offered together.
    test('is chosen instead of namespaces, and they instead of it', async () => {
        const { page } = await mountMixed();
        page.shadowRoot!.querySelector<HTMLButtonElement>('.multiselect-btn')!.click();
        await page.updateComplete;
        const checked = () => Array.from(page.shadowRoot!.querySelectorAll('.multiselect-option'))
            .filter(o => o.querySelector<HTMLInputElement>('input[type="checkbox"]')!.checked)
            .map(o => o.textContent!.trim());

        toggleOption(page, 'ns-c');
        await page.updateComplete;
        expect(checked()).toEqual(['ns-c']);

        toggleOption(page, 'Unidentified peers');
        await page.updateComplete;
        expect(checked()).toEqual(['Unidentified peers']);

        toggleOption(page, 'ns-c');
        await page.updateComplete;
        expect(checked()).toEqual(['ns-c']);
    });

    // Nothing is redacted in cluster scope, so there it could only ever match nothing.
    test('is not offered in cluster scope, and is dropped on moving there', async () => {
        const { page } = await mountMixed();
        await applyUnidentifiedPeers(page);
        expect(page.shadowRoot!.querySelectorAll('tbody tr')).toHaveLength(1);

        // The fixture does not hold cluster scope, so move there the way a shared link would.
        (page as unknown as { _onScopeChange(v: string): void })._onScopeChange('__cluster_wide__');
        await page.updateComplete;
        await vi.advanceTimersByTimeAsync(0);
        await page.updateComplete;

        expect((page as unknown as { _unidentifiedPeersOnly: boolean })._unidentifiedPeersOnly).toBe(false);
        page.shadowRoot!.querySelector<HTMLButtonElement>('.multiselect-btn')!.click();
        await page.updateComplete;
        expect(peerNamespaceOptions(page)).not.toContain('Unidentified peers');
    });

    test('it also narrows the service map', async () => {
        const { page } = await mountMixed();
        await applyUnidentifiedPeers(page);
        page.viewMode = 'map';
        await page.updateComplete;

        const graph = (page as unknown as { _graphRef: { nodes: { kind: string }[] } })._graphRef;
        expect(graph.nodes.filter(n => n.kind === 'undisclosedNamespace')).toHaveLength(1);
        expect(graph.nodes).toHaveLength(2);
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
        const row = await rowFor(makeFlow({ dstDisclosure: EndpointDisclosure.Flow, dstNs: 'ns-c', dstPod: '' }));
        const svc = cell(row, 3);
        expect(svc.querySelector('.redacted')).not.toBeNull();
        expect(svc.querySelector('.redacted')!.getAttribute('title')).toContain("destination's namespace");
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
    interface TestNode { id: string; shortName: string; kind: string; detail: string }
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
