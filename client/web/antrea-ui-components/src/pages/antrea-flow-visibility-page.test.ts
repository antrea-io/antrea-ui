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
import type { AntreaFlowVisibilityPage } from './antrea-flow-visibility-page';
import type { FlowStreamFilter } from '../lib/flow-stream';
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
    srcPod?: string;
    dstPod?: string;
    ingressPolicy?: string;
    ingressAction?: NetworkPolicyRuleAction;
    srcIP?: string;
    dstIP?: string;
    srcNs?: string;
    dstNs?: string;
    srcDisclosure?: EndpointDisclosure;
    dstDisclosure?: EndpointDisclosure;
    destService?: string;
}

function makeFlow(overrides: FlowOverrides = {}): Flow {
    return {
        id: `flow-${Math.random()}`,
        startTs: '2026-03-25T00:00:00Z',
        endTs: '2026-03-25T00:01:00Z',
        endReason: FlowEndReason.Unspecified,
        // connectionKey() (flow-types.ts) is keyed on source/destination IP, not pod name —
        // entries with distinct pod names but the same IPs collapse into a single FlowStore
        // entry. Tests distinguishing entries by pod name must also vary the source IP.
        ip: { version: IPVersion.IPv4, source: overrides.srcIP ?? '10.0.0.1', destination: overrides.dstIP ?? '10.0.0.2' },
        transport: { protocolNumber: 6, sourcePort: 12345, destinationPort: 80 },
        k8s: {
            flowType: FlowType.InterNode,
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
            ingressNetworkPolicyType: overrides.ingressPolicy ? NetworkPolicyType.K8s : NetworkPolicyType.Unspecified,
            ingressNetworkPolicyNamespace: '',
            ingressNetworkPolicyName: overrides.ingressPolicy ?? '',
            ingressNetworkPolicyUid: '',
            ingressNetworkPolicyRuleName: '',
            ingressNetworkPolicyRuleAction: overrides.ingressAction ?? (overrides.ingressPolicy ? NetworkPolicyRuleAction.Allow : NetworkPolicyRuleAction.NoAction),
            egressNetworkPolicyType: NetworkPolicyType.Unspecified,
            egressNetworkPolicyNamespace: '',
            egressNetworkPolicyName: '',
            egressNetworkPolicyUid: '',
            egressNetworkPolicyRuleName: '',
            egressNetworkPolicyRuleAction: NetworkPolicyRuleAction.NoAction,
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
    // The page writes the scope into the URL, so a test that selected one would otherwise leak
    // it into the next through the shared jsdom location.
    window.history.replaceState(null, '', '/');
});

function jsonResponse(body: unknown, status = 200): Response {
    return new Response(JSON.stringify(body), { status });
}

/** A GET /api/v1/flows/namespaces body. The default offers the namespace the fixtures' flows are
 * in, so a test that does not care about the selector still gets a stream. */
function flowNamespacesResponse(
    opts: { namespaces?: { namespace: string; canObserve: boolean }[]; clusterWide?: boolean; incomplete?: boolean } = {},
): Response {
    return jsonResponse({
        namespaces: opts.namespaces ?? [{ namespace: 'default', canObserve: true }],
        clusterWide: opts.clusterWide ?? false,
        incomplete: opts.incomplete ?? false,
    });
}

// Does NOT advance timers: the stream only opens once the settings fetch in connectedCallback()
// resolves, so a test that wants to observe or count that first stream request must attach its
// listeners before letting the microtask queue drain.
// `search` is the page URL's query string, which is where the observed namespace lives: the
// default selects one, since a page with no scope selected deliberately opens no stream at all
// and almost every test here is about a stream that is open. GET /api/v1/flows/namespaces is
// answered ahead of `fetchImpl` unless that already handles it, so the individual tests only
// have to describe the stream.
async function mount(
    fetchImpl: (url: string, init?: RequestInit) => Promise<Response>,
    opts: { search?: string; flowNamespaces?: () => Response } = {},
): Promise<AntreaFlowVisibilityPage> {
    window.history.replaceState(null, '', opts.search ?? '/?observedNamespace=default');
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
        if (typeof url === 'string' && url.includes('/api/v1/flows/namespaces')) {
            return (opts.flowNamespaces ?? flowNamespacesResponse)();
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

// The observed-namespace selector, which replaced the hardcoded cluster scope this page used
// while there was no UI to pick one. The Flow Aggregator requires every stream to name exactly
// one scope and rejects a request naming none with a 400, so the selector is not a filter: with
// nothing selected the page opens no stream at all.
describe('AntreaFlowVisibilityPage — observed-namespace selector', () => {
    function scopeSelect(page: AntreaFlowVisibilityPage): HTMLSelectElement {
        return page.shadowRoot!.querySelector<HTMLSelectElement>('#observed-ns')!;
    }

    test('the stream names the namespace the URL carries', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        await mount(fetchMock, { search: '/?observedNamespace=ns-a' });

        const calls = streamCalls(fetchMock);
        expect(calls).toHaveLength(1);
        const params = new URL(calls[0][0] as string, 'http://example.test').searchParams;
        expect(params.get('observedNamespace')).toBe('ns-a');
        expect(params.get('clusterWide')).toBeNull();
    });

    test('no stream opens while no scope is selected', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        const page = await mount(fetchMock, { search: '/' });

        expect(streamCalls(fetchMock)).toHaveLength(0);
        // And it stays that way: nothing retries into a scope-less request later either.
        await vi.advanceTimersByTimeAsync(60_000);
        expect(streamCalls(fetchMock)).toHaveLength(0);
        expect(page.shadowRoot!.textContent).toContain('Select a namespace to observe');
        expect(page.shadowRoot!.querySelector('tbody')).toBeNull();
    });

    test('the map view opens no stream either, and offers the same selector', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        const page = await mount(fetchMock, { search: '/' });
        page.viewMode = 'map';
        await page.updateComplete;

        expect(streamCalls(fetchMock)).toHaveLength(0);
        expect(page.shadowRoot!.querySelector('#graph-svg')).toBeNull();
        expect(scopeSelect(page)).not.toBeNull();
    });

    test('selecting a namespace opens the stream and records the scope in the URL', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        const page = await mount(fetchMock, { search: '/' });
        expect(streamCalls(fetchMock)).toHaveLength(0);

        const select = scopeSelect(page);
        select.value = 'default';
        select.dispatchEvent(new Event('change', { bubbles: true }));
        await page.updateComplete;
        await vi.advanceTimersByTimeAsync(0);

        const calls = streamCalls(fetchMock);
        expect(calls).toHaveLength(1);
        expect(new URL(calls[0][0] as string, 'http://example.test').searchParams.get('observedNamespace'))
            .toBe('default');
        expect(window.location.search).toContain('observedNamespace=default');
    });

    test('changing the scope reconnects with the new namespace', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        const page = await mount(fetchMock, {
            search: '/?observedNamespace=default',
            flowNamespaces: () => flowNamespacesResponse({
                namespaces: [{ namespace: 'default', canObserve: true }, { namespace: 'ns-b', canObserve: true }],
            }),
        });
        expect(streamCalls(fetchMock)).toHaveLength(1);

        const select = scopeSelect(page);
        select.value = 'ns-b';
        select.dispatchEvent(new Event('change', { bubbles: true }));
        await page.updateComplete;
        await vi.advanceTimersByTimeAsync(0);

        const calls = streamCalls(fetchMock);
        expect(calls).toHaveLength(2);
        expect(new URL(calls[1][0] as string, 'http://example.test').searchParams.get('observedNamespace'))
            .toBe('ns-b');
    });

    test('cluster scope is offered only when the backend says the user holds it', async () => {
        const page = await mount(async () => sseResponse([]), {
            search: '/',
            flowNamespaces: () => flowNamespacesResponse({ clusterWide: false }),
        });
        expect(Array.from(scopeSelect(page).options).map(o => o.value)).not.toContain('__cluster_wide__');

        const withCluster = await mount(async () => sseResponse([]), {
            search: '/',
            flowNamespaces: () => flowNamespacesResponse({ clusterWide: true }),
        });
        const clusterOption = Array.from(scopeSelect(withCluster).options).find(o => o.value === '__cluster_wide__');
        expect(clusterOption).toBeDefined();
        expect(clusterOption!.textContent).toContain('All namespaces');
    });

    test('selecting cluster scope asks for clusterWide, not a namespace', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        const page = await mount(fetchMock, {
            search: '/',
            flowNamespaces: () => flowNamespacesResponse({ clusterWide: true }),
        });

        const select = scopeSelect(page);
        select.value = '__cluster_wide__';
        select.dispatchEvent(new Event('change', { bubbles: true }));
        await page.updateComplete;
        await vi.advanceTimersByTimeAsync(0);

        const params = new URL(streamCalls(fetchMock)[0][0] as string, 'http://example.test').searchParams;
        expect(params.get('clusterWide')).toBe('true');
        expect(params.get('observedNamespace')).toBeNull();
        expect(window.location.search).toContain('clusterWide=true');
    });

    // A user who may observe no namespace is a real answer the backend is entitled to give, not a
    // failure: it has to read as an explanation rather than an error.
    test('an empty namespace list is an explanation, not an error', async () => {
        const page = await mount(async () => sseResponse([]), {
            search: '/',
            flowNamespaces: () => flowNamespacesResponse({ namespaces: [], clusterWide: false, incomplete: true }),
        });

        expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')).toBeNull();
        expect(page.shadowRoot!.textContent).toContain('not authorized to observe flows in any namespace');
        // And the note says what to do about it, not just that the list is unreliable: the
        // URL parameter is a real escape hatch, honoured whether or not the namespace is listed.
        expect(page.shadowRoot!.textContent).toContain('may be missing namespaces you can observe');
        expect(page.shadowRoot!.textContent).toContain('Namespace not listed');
    });

    // The escape hatch for an incomplete list, and the reason the note no longer tells anyone to
    // hand-edit a URL.
    test('a namespace named in the free-text box opens a stream for it', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        const page = await mount(fetchMock, {
            search: '/',
            flowNamespaces: () => flowNamespacesResponse({ namespaces: [], incomplete: true }),
        });
        expect(streamCalls(fetchMock)).toHaveLength(0);

        const input = page.shadowRoot!.querySelector<HTMLInputElement>('#observed-ns-by-name')!;
        // Padded on purpose: a name pasted from a terminal usually is.
        input.value = '  ns-unlisted  ';
        input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
        await page.updateComplete;

        expect(streamCalls(fetchMock)).toHaveLength(1);
        expect(String(streamCalls(fetchMock)[0][0])).toContain('observedNamespace=ns-unlisted');
        // Cleared, so the box does not sit there looking like the current scope - the select
        // below it is what shows that.
        expect(input.value).toBe('');
        expect(scopeSelect(page).value).toBe('ns-unlisted');
    });

    // replaceState changes the URL without notifying anyone, so a router reading the location
    // through its own abstraction keeps serving a stale value and builds links carrying a stale
    // scope, or none at all. Both host routers resync on popstate; without this the Service Map
    // link silently dropped the scope.
    test('writing the scope to the URL notifies the router', async () => {
        const seen: string[] = [];
        const onPop = () => seen.push(window.location.search);
        window.addEventListener('popstate', onPop);
        try {
            const page = await mount(async () => sseResponse([]), {
                search: '/',
                flowNamespaces: () => flowNamespacesResponse(),
            });
            scopeSelect(page).value = 'default';
            scopeSelect(page).dispatchEvent(new Event('change', { bubbles: true }));
            await page.updateComplete;

            expect(seen).toContain('?observedNamespace=default');
        } finally {
            window.removeEventListener('popstate', onPop);
        }
    });

    // Writing the same URL again would fire popstate for nothing, and a router that re-renders
    // on it would do so for every unrelated change.
    test('an unchanged URL fires no popstate', async () => {
        let pops = 0;
        const onPop = () => { pops += 1; };
        window.addEventListener('popstate', onPop);
        try {
            const page = await mount(async () => sseResponse([]), {
                search: '/?observedNamespace=default',
                flowNamespaces: () => flowNamespacesResponse(),
            });
            await page.updateComplete;
            expect(pops).toBe(0);
        } finally {
            window.removeEventListener('popstate', onPop);
        }
    });

    test('the free-text box is absent when the list is exhaustive', async () => {
        // Not tidiness: if the list is complete, a namespace missing from it is one this user
        // cannot observe, so the box could only invite a request that will be refused.
        const page = await mount(async () => sseResponse([]), {
            search: '/',
            flowNamespaces: () => flowNamespacesResponse({ incomplete: false }),
        });

        expect(page.shadowRoot!.querySelector('#observed-ns-by-name')).toBeNull();
    });

    test('an empty free-text box does nothing', async () => {
        const fetchMock = vi.fn(async () => sseResponse([]));
        const page = await mount(fetchMock, {
            search: '/',
            flowNamespaces: () => flowNamespacesResponse({ namespaces: [], incomplete: true }),
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
            search: '/?observedNamespace=ns-unlisted',
            flowNamespaces: () => flowNamespacesResponse({ namespaces: [], incomplete: true }),
        });

        expect(streamCalls(fetchMock)).toHaveLength(1);
        expect(scopeSelect(page).value).toBe('ns-unlisted');
    });

    // Namespaces the backend reported as unobservable are shown refused rather than omitted, so
    // a user looking for one finds out why it is not available.
    test('a namespace the user cannot observe is offered as a disabled option', async () => {
        const page = await mount(async () => sseResponse([]), {
            search: '/',
            flowNamespaces: () => flowNamespacesResponse({
                namespaces: [{ namespace: 'default', canObserve: true }, { namespace: 'locked', canObserve: false }],
            }),
        });
        const locked = Array.from(scopeSelect(page).options).find(o => o.value === 'locked')!;
        expect(locked.disabled).toBe(true);
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
        const page = await mount(fetchMock, { search: '/?observedNamespace=ns-a' });
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
        const page = await mount(async () => sseResponse([flowEventChunk([makeFlow()])]), { search: '/?observedNamespace=ns-a' });
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
    // different request from ever being tried. Today every request hardcodes cluster-wide scope,
    // so nothing changes _which_ scope is asked for, but a peer-filter change is already enough to
    // prove the guard is keyed to the request that was refused rather than latched for the page's
    // lifetime - _applyFilter's own early return on an unchanged filter key would otherwise make
    // this indistinguishable from "the restriction survives a stream restart" above.
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

// The namespace multi-select is a peer filter now, not a second scope control: it selects flows
// by their far end, which upstream explicitly supports naming an out-of-scope namespace for.
describe('AntreaFlowVisibilityPage — peer namespace filter', () => {
    async function mountWithPeers(search = '/?observedNamespace=ns-a'): Promise<AntreaFlowVisibilityPage> {
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
        const page = await mountWithPeers('/?observedNamespace=default');
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
        const page = await mount(fetchMock, { search: '/?observedNamespace=default' });
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
        expect(dst.querySelector('.redacted')!.getAttribute('title')).toContain('get flows/identity on ns-c');
    });

    // The denied connection: redactFlow clears the peer's namespace too, so only the address is
    // left — and there is no namespace to name, so no tooltip either.
    test('a Flow-tier peer on a denied connection shows the address alone, with no tooltip', async () => {
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

    // Form B: a denied connection costs the peer its namespace too, so there is nothing left to
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
        // Distinct flow IDs, not records, and the bytes of both flows.
        expect(graph.edges[0].connectionCount).toBe(2);
        expect(graph.edges[0].totalBytesForward).toBe(200);
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
        expect(labels[0]).toContain('get flows/identity on ns-c');
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
