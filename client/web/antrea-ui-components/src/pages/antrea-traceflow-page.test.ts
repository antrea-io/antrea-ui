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
import './antrea-traceflow-page';
import type { AntreaTraceflowPage } from './antrea-traceflow-page';

let el: AntreaTraceflowPage | undefined;

afterEach(() => {
    el?.remove();
    el = undefined;
    vi.unstubAllGlobals();
});

async function mount(): Promise<AntreaTraceflowPage> {
    el = document.createElement('antrea-traceflow-page') as AntreaTraceflowPage;
    el.token = 'my-token';
    document.body.appendChild(el);
    await el.updateComplete;
    return el;
}

function checkboxByLabel(root: ShadowRoot, labelText: string): HTMLInputElement {
    const label = Array.from(root.querySelectorAll('.checkbox-label'))
        .find(l => l.textContent?.trim().startsWith(labelText));
    if (!label) throw new Error(`no checkbox labeled "${labelText}"`);
    return label.querySelector('input')!;
}

async function setProto(page: AntreaTraceflowPage, proto: 'TCP' | 'UDP' | 'ICMP') {
    const select = page.shadowRoot!.querySelector<HTMLSelectElement>('#protocol')!;
    select.value = proto;
    select.dispatchEvent(new Event('change', { bubbles: true }));
    await page.updateComplete;
}

async function setLive(page: AntreaTraceflowPage, checked: boolean) {
    const checkbox = checkboxByLabel(page.shadowRoot!, 'Live Traffic');
    checkbox.checked = checked;
    checkbox.dispatchEvent(new Event('change', { bubbles: true }));
    await page.updateComplete;
}

function setInput(page: AntreaTraceflowPage, id: string, value: string) {
    const input = page.shadowRoot!.querySelector<HTMLInputElement>(`#${id}`)!;
    input.value = value;
    input.dispatchEvent(new Event('input', { bubbles: true }));
}

function selectDestinationType(page: AntreaTraceflowPage, type: 'Pod' | 'Service' | 'IP') {
    const radio = Array.from(page.shadowRoot!.querySelectorAll<HTMLInputElement>('input[name="dst-type"]'))
        .find(r => r.value === type)!;
    radio.checked = true;
    radio.dispatchEvent(new Event('change', { bubbles: true }));
}

function setCheckbox(page: AntreaTraceflowPage, labelText: string, checked: boolean) {
    const checkbox = checkboxByLabel(page.shadowRoot!, labelText);
    checkbox.checked = checked;
    checkbox.dispatchEvent(new Event('change', { bubbles: true }));
}

describe('AntreaTraceflowPage — form field visibility', () => {
    test.each([
        {
            name: 'TCP, not live',
            proto: 'TCP' as const, live: false,
            mustBePresent: ['#src-port', '#tcp-flags'],
            mustNotBePresent: ['dropped'],
        },
        {
            name: 'UDP, not live',
            proto: 'UDP' as const, live: false,
            mustBePresent: ['#src-port'],
            mustNotBePresent: ['#tcp-flags', 'dropped'],
        },
        {
            name: 'ICMP, not live',
            proto: 'ICMP' as const, live: false,
            mustBePresent: [],
            mustNotBePresent: ['#src-port', '#tcp-flags', 'dropped'],
        },
        {
            name: 'TCP, live',
            proto: 'TCP' as const, live: true,
            mustBePresent: ['#src-port', 'dropped'],
            mustNotBePresent: ['#tcp-flags'],
        },
        {
            name: 'UDP, live',
            proto: 'UDP' as const, live: true,
            mustBePresent: ['#src-port', 'dropped'],
            mustNotBePresent: ['#tcp-flags'],
        },
        {
            name: 'ICMP, live',
            proto: 'ICMP' as const, live: true,
            mustBePresent: ['dropped'],
            mustNotBePresent: ['#src-port', '#tcp-flags'],
        },
    ])('$name', async ({ proto, live, mustBePresent, mustNotBePresent }) => {
        const page = await mount();
        await setProto(page, proto);
        await setLive(page, live);

        const has = (selector: string) => selector === 'dropped'
            ? !!Array.from(page.shadowRoot!.querySelectorAll('.checkbox-label')).find(l => l.textContent?.includes('Dropped Traffic Only'))
            : page.shadowRoot!.querySelector(selector) !== null;

        mustBePresent.forEach(s => expect(has(s)).toBe(true));
        mustNotBePresent.forEach(s => expect(has(s)).toBe(false));
    });
});

describe('AntreaTraceflowPage — building and submitting the Traceflow request', () => {
    function mockTraceflowFetch() {
        const calls: { url: string; init?: RequestInit }[] = [];
        const fn = vi.fn(async (url: string, init?: RequestInit) => {
            calls.push({ url, init });
            if (url === '/api/v1/traceflow' && init?.method === 'POST') {
                return {
                    ok: true,
                    status: 202,
                    statusText: 'Accepted',
                    url,
                    headers: {
                        get: (k: string) => (k.toLowerCase() === 'location'
                            ? '/api/v1/apis/crd.antrea.io/v1beta1/traceflows/tf-test'
                            : k.toLowerCase() === 'retry-after' ? '0' : null),
                    },
                    text: async () => '',
                    json: async () => ({}),
                } as unknown as Response;
            }
            if (url.endsWith('/status')) {
                // Report immediate completion so the poll loop only runs once.
                return {
                    ok: true,
                    status: 200,
                    statusText: 'OK',
                    url: url.replace('/status', '/result'),
                    headers: { get: () => null },
                    text: async () => '',
                    json: async () => ({ status: { phase: 'Running', reason: '', startTime: '', results: [] } }),
                } as unknown as Response;
            }
            if (init?.method === 'DELETE') {
                return { ok: true, status: 200, statusText: 'OK', url, headers: { get: () => null }, text: async () => '' } as unknown as Response;
            }
            throw new Error(`unexpected fetch: ${init?.method ?? 'GET'} ${url}`);
        });
        return { fn, calls };
    }

    test('regular Traceflow: Pod source, Service destination, TCP, IPv6', async () => {
        const { fn, calls } = mockTraceflowFetch();
        vi.stubGlobal('fetch', fn);
        const page = await mount();

        setInput(page, 'src-ns', 'namespaceA');
        setInput(page, 'src', 'podA');
        await setProto(page, 'TCP');
        setCheckbox(page, 'Use IPv6', true);
        selectDestinationType(page, 'Service');
        setInput(page, 'dst-ns', 'namespaceA');
        setInput(page, 'dst', 'serviceA');
        setInput(page, 'dst-port', '80');
        await page.updateComplete;

        page.shadowRoot!.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
        await page.updateComplete;
        await new Promise(r => setTimeout(r, 150));
        await page.updateComplete;

        const createCall = calls.find(c => c.url === '/api/v1/traceflow');
        expect(createCall).toBeDefined();
        const sentSpec = JSON.parse(createCall!.init!.body as string).spec;
        expect(sentSpec).toEqual({
            source: { namespace: 'namespaceA', pod: 'podA' },
            destination: { namespace: 'namespaceA', service: 'serviceA' },
            packet: {
                ipv6Header: { nextHeader: 6 },
                transportHeader: { tcp: { dstPort: 80, flags: 2 } },
            },
            timeout: 20,
        });
    });

    test('live Traceflow: dropped-only, UDP, Pod destination', async () => {
        const { fn, calls } = mockTraceflowFetch();
        vi.stubGlobal('fetch', fn);
        const page = await mount();

        await setLive(page, true);
        setCheckbox(page, 'Dropped Traffic Only', true);
        await setProto(page, 'UDP');
        selectDestinationType(page, 'Pod');
        setInput(page, 'dst-ns', 'namespaceA');
        setInput(page, 'dst', 'podA');
        setInput(page, 'timeout', '120');
        await page.updateComplete;

        page.shadowRoot!.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
        await page.updateComplete;
        await new Promise(r => setTimeout(r, 150));
        await page.updateComplete;

        const createCall = calls.find(c => c.url === '/api/v1/traceflow');
        expect(createCall).toBeDefined();
        const sentSpec = JSON.parse(createCall!.init!.body as string).spec;
        expect(sentSpec).toEqual({
            source: {},
            destination: { namespace: 'namespaceA', pod: 'podA' },
            packet: {
                ipHeader: { protocol: 17 },
                transportHeader: { udp: {} },
            },
            liveTraffic: true,
            droppedOnly: true,
            timeout: 120,
        });
    });

    test('removing the element mid-poll stops further polling', async () => {
        const calls: { url: string; init?: RequestInit }[] = [];
        const fn = vi.fn(async (url: string, init?: RequestInit) => {
            calls.push({ url, init });
            if (url === '/api/v1/traceflow' && init?.method === 'POST') {
                return {
                    ok: true, status: 202, statusText: 'Accepted', url,
                    headers: {
                        get: (k: string) => (k.toLowerCase() === 'location'
                            ? '/api/v1/apis/crd.antrea.io/v1beta1/traceflows/tf-test'
                            : k.toLowerCase() === 'retry-after' ? '0' : null),
                    },
                    text: async () => '', json: async () => ({}),
                } as unknown as Response;
            }
            if (url.endsWith('/status')) {
                // Never reports completion, so the loop keeps polling until cancelled.
                return {
                    ok: true, status: 200, statusText: 'OK', url,
                    headers: { get: () => null }, text: async () => '',
                } as unknown as Response;
            }
            throw new Error(`unexpected fetch: ${init?.method ?? 'GET'} ${url}`);
        });
        vi.stubGlobal('fetch', fn);
        const page = await mount();

        setInput(page, 'src', 'podA');
        setInput(page, 'dst', 'podB');
        await page.updateComplete;
        page.shadowRoot!.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));

        // Let a couple of poll iterations happen (each waits ~100ms, since retry-after=0).
        await new Promise(r => setTimeout(r, 250));
        const callsBeforeRemoval = calls.length;
        expect(callsBeforeRemoval).toBeGreaterThan(1);

        page.remove();
        el = undefined;

        await new Promise(r => setTimeout(r, 300));
        expect(calls.length).toBe(callsBeforeRemoval);
    });

    test('validation error is shown and no request is sent when source and destination are both empty', async () => {
        const fetchMock = vi.fn();
        vi.stubGlobal('fetch', fetchMock);
        const page = await mount();

        page.shadowRoot!.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
        await page.updateComplete;

        expect(fetchMock).not.toHaveBeenCalled();
        expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')?.textContent)
            .toContain('required');
    });
});
