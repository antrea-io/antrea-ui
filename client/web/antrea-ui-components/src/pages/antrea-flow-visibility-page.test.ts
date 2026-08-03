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
import {
    Flow,
    FlowType,
    FlowEndReason,
    IPVersion,
    NetworkPolicyType,
    NetworkPolicyRuleAction,
} from '../lib/flow-types';

function makeFlow(overrides: { srcPod?: string; dstPod?: string; ingressPolicy?: string; srcIP?: string } = {}): Flow {
    return {
        id: `flow-${Math.random()}`,
        startTs: '2026-03-25T00:00:00Z',
        endTs: '2026-03-25T00:01:00Z',
        endReason: FlowEndReason.Unspecified,
        // connectionKey() (flow-types.ts) is keyed on source/destination IP, not pod name —
        // entries with distinct pod names but the same IPs collapse into a single FlowStore
        // entry. Tests distinguishing entries by pod name must also vary the source IP.
        ip: { version: IPVersion.IPv4, source: overrides.srcIP ?? '10.0.0.1', destination: '10.0.0.2' },
        transport: { protocolNumber: 6, sourcePort: 12345, destinationPort: 80 },
        k8s: {
            flowType: FlowType.InterNode,
            sourcePodNamespace: 'default',
            sourcePodName: overrides.srcPod ?? 'client-abc12',
            sourcePodUid: '',
            sourceNodeName: 'node-1',
            sourceNodeUid: '',
            destinationPodNamespace: 'default',
            destinationPodName: overrides.dstPod ?? 'server-xyz34',
            destinationPodUid: '',
            destinationNodeName: 'node-2',
            destinationNodeUid: '',
            destinationClusterIp: '',
            destinationServicePort: 0,
            destinationServicePortName: '',
            destinationServiceUid: '',
            ingressNetworkPolicyType: overrides.ingressPolicy ? NetworkPolicyType.K8s : NetworkPolicyType.Unspecified,
            ingressNetworkPolicyNamespace: '',
            ingressNetworkPolicyName: overrides.ingressPolicy ?? '',
            ingressNetworkPolicyUid: '',
            ingressNetworkPolicyRuleName: '',
            ingressNetworkPolicyRuleAction: overrides.ingressPolicy ? NetworkPolicyRuleAction.Allow : NetworkPolicyRuleAction.NoAction,
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
});

// Does NOT advance timers: the stream only opens once the settings fetch in connectedCallback()
// resolves, so a test that wants to observe or count that first stream request must attach its
// listeners before letting the microtask queue drain.
async function mount(fetchImpl: (url: string, init?: RequestInit) => Promise<Response>): Promise<AntreaFlowVisibilityPage> {
    vi.stubGlobal('fetch', vi.fn(fetchImpl));
    el = document.createElement('antrea-flow-visibility-page') as AntreaFlowVisibilityPage;
    document.body.appendChild(el);
    await el.updateComplete;
    return el;
}

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

describe('AntreaFlowVisibilityPage — antrea-edge-selected extension point', () => {
    async function mountWithOneEdge(): Promise<AntreaFlowVisibilityPage> {
        const page = await mount(async () => sseResponse([flowEventChunk([makeFlow({ ingressPolicy: 'allow-client' })])]));
        await page.updateComplete;
        await vi.advanceTimersByTimeAsync(0);
        await vi.advanceTimersByTimeAsync(1000);
        // Switch to map view — this is what triggers _buildServiceMap(), synchronously
        // populating _graphRef.edgeMap and wiring up click handlers on the rendered paths
        // (independent of the D3 force simulation, which only animates positions afterward).
        (page as unknown as { _viewMode: string })._viewMode = 'map';
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
        const page = await mount(fetchMock);
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
        const checkbox = page.shadowRoot!.querySelector<HTMLInputElement>('.multiselect-option input[type="checkbox"]')!;
        checkbox.dispatchEvent(new Event('change', { bubbles: true }));
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
        page.shadowRoot!.querySelector<HTMLInputElement>('.multiselect-option input[type="checkbox"]')!
            .dispatchEvent(new Event('change', { bubbles: true }));
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
        const page = await mount(async () => sseResponse([flowEventChunk([makeFlow()])]));
        await page.updateComplete;
        await vi.advanceTimersByTimeAsync(0);
        await vi.advanceTimersByTimeAsync(1000);

        const toggle = page.shadowRoot!.querySelector<HTMLButtonElement>('.multiselect-btn')!;
        expect(page.shadowRoot!.querySelector('.multiselect-dropdown')).toBeNull();

        toggle.click();
        await page.updateComplete;
        expect(page.shadowRoot!.querySelector('.multiselect-dropdown')).not.toBeNull();
        expect(toggle.textContent).toContain('All');

        const checkbox = page.shadowRoot!.querySelector<HTMLInputElement>('.multiselect-option input[type="checkbox"]')!;
        checkbox.dispatchEvent(new Event('change', { bubbles: true }));
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
