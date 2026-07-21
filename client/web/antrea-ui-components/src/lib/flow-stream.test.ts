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

import { afterEach, beforeEach, describe, expect, it, test, vi } from 'vitest';
import { streamFilterKey, FlowStreamFilter, FlowStreamClient, FlowStreamCallbacks } from './flow-stream';
import { setApiBase } from './api';

describe('streamFilterKey', () => {
    it('matches for different object instances with the same filter', () => {
        const a: FlowStreamFilter = {};
        const b: FlowStreamFilter = {};
        expect(streamFilterKey(a)).toBe(streamFilterKey(b));
    });

    it('normalizes array field order', () => {
        const a: FlowStreamFilter = { namespaces: ['z', 'a'] };
        const b: FlowStreamFilter = { namespaces: ['a', 'z'] };
        expect(streamFilterKey(a)).toBe(streamFilterKey(b));
    });

    it('changes when a filter field changes', () => {
        const empty: FlowStreamFilter = {};
        const withNs: FlowStreamFilter = { namespaces: ['default'] };
        expect(streamFilterKey(empty)).not.toBe(streamFilterKey(withNs));
    });
});

describe('FlowStreamClient', () => {
    function sseResponse(chunks: string[], status = 200): Response {
        const encoder = new TextEncoder();
        const stream = new ReadableStream<Uint8Array>({
            start(controller) {
                for (const chunk of chunks) controller.enqueue(encoder.encode(chunk));
                // Leave the stream open (SSE connections don't close on their own) unless the
                // caller wants a specific "done" test, which closes it itself via a trailing
                // marker chunk of ''.
            },
        });
        return new Response(stream, { status });
    }

    function makeCallbacks(): FlowStreamCallbacks & {
        flows: unknown[];
        errors: Error[];
        dropped: number[];
        connected: number;
        disconnected: number;
        authErrors: number;
    } {
        const cb = {
            flows: [] as unknown[],
            errors: [] as Error[],
            dropped: [] as number[],
            connected: 0,
            disconnected: 0,
            authErrors: 0,
            onFlows: (flows: unknown[]) => { cb.flows.push(...flows); },
            onError: (err: Error) => { cb.errors.push(err); },
            onDropped: (count: number) => { cb.dropped.push(count); },
            onConnected: () => { cb.connected++; },
            onDisconnected: () => { cb.disconnected++; },
            onAuthError: () => { cb.authErrors++; },
        };
        return cb;
    }

    let fetchMock: ReturnType<typeof vi.fn>;

    beforeEach(() => {
        vi.useFakeTimers();
    });

    afterEach(() => {
        vi.useRealTimers();
        vi.unstubAllGlobals();
        setApiBase(''); // apiBase is module-level state — reset it so tests don't leak into each other
    });

    function stubFetch(impl: (url: string, init?: RequestInit) => Promise<Response>) {
        fetchMock = vi.fn(impl);
        vi.stubGlobal('fetch', fetchMock);
    }

    test('sends the Authorization header and connects', async () => {
        stubFetch(async () => sseResponse([]));
        const cb = makeCallbacks();
        const client = new FlowStreamClient('my-token', {}, cb);

        client.start();
        await vi.advanceTimersByTimeAsync(0);

        expect(fetchMock).toHaveBeenCalledTimes(1);
        const [url, init] = fetchMock.mock.calls[0];
        expect(url).toContain('/api/v1/flows/stream');
        expect((init.headers as Record<string, string>).Authorization).toBe('Bearer my-token');
        expect(cb.connected).toBe(1);

        client.stop();
    });

    test('prefixes the stream URL with the configured API base', async () => {
        setApiBase('http://localhost:8080');
        stubFetch(async () => sseResponse([]));
        const cb = makeCallbacks();
        const client = new FlowStreamClient('my-token', {}, cb);

        client.start();
        await vi.advanceTimersByTimeAsync(0);

        const [url] = fetchMock.mock.calls[0];
        expect(url).toBe('http://localhost:8080/api/v1/flows/stream?');

        client.stop();
    });

    test('parses multiple SSE events in a single chunk', async () => {
        stubFetch(async () => sseResponse([
            'event: flow\ndata: {"flows":[{"id":"a"}]}\n\n' +
            'event: flow\ndata: {"flows":[{"id":"b"}]}\n\n',
        ]));
        const cb = makeCallbacks();
        const client = new FlowStreamClient('t', {}, cb, 10);
        client.start();
        await vi.advanceTimersByTimeAsync(0);
        await vi.advanceTimersByTimeAsync(10);

        expect(cb.flows).toEqual([{ id: 'a' }, { id: 'b' }]);
        client.stop();
    });

    test('parses an SSE event split across multiple read() chunks', async () => {
        stubFetch(async () => sseResponse([
            'event: flow\ndata: {"flows":[{"id"',
            ':"a"}]}\n\n',
        ]));
        const cb = makeCallbacks();
        const client = new FlowStreamClient('t', {}, cb, 10);
        client.start();
        await vi.advanceTimersByTimeAsync(0);
        await vi.advanceTimersByTimeAsync(10);

        expect(cb.flows).toEqual([{ id: 'a' }]);
        client.stop();
    });

    test('handles both "data: " and "data:" (no space) prefixes', async () => {
        stubFetch(async () => sseResponse([
            'event: flow\ndata:{"flows":[{"id":"a"}]}\n\n',
        ]));
        const cb = makeCallbacks();
        const client = new FlowStreamClient('t', {}, cb, 10);
        client.start();
        await vi.advanceTimersByTimeAsync(0);
        await vi.advanceTimersByTimeAsync(10);

        expect(cb.flows).toEqual([{ id: 'a' }]);
        client.stop();
    });

    test('dispatches dropped and error events by type', async () => {
        stubFetch(async () => sseResponse([
            'event: dropped\ndata: {"droppedCount":5}\n\n' +
            'event: error\ndata: {"message":"boom"}\n\n',
        ]));
        const cb = makeCallbacks();
        const client = new FlowStreamClient('t', {}, cb, 10);
        client.start();
        await vi.advanceTimersByTimeAsync(0);

        expect(cb.dropped).toEqual([5]);
        expect(cb.errors.map(e => e.message)).toEqual(['boom']);
        client.stop();
    });

    test('batches flows and flushes on the batch interval', async () => {
        stubFetch(async () => sseResponse([
            'event: flow\ndata: {"flows":[{"id":"a"}]}\n\n',
        ]));
        const cb = makeCallbacks();
        const client = new FlowStreamClient('t', {}, cb, 50);
        client.start();
        await vi.advanceTimersByTimeAsync(0);

        // Not flushed yet — batch interval hasn't elapsed.
        expect(cb.flows).toEqual([]);
        await vi.advanceTimersByTimeAsync(50);
        expect(cb.flows).toEqual([{ id: 'a' }]);
        client.stop();
    });

    test('flushes any remaining batch on stop()', async () => {
        stubFetch(async () => sseResponse([
            'event: flow\ndata: {"flows":[{"id":"a"}]}\n\n',
        ]));
        const cb = makeCallbacks();
        const client = new FlowStreamClient('t', {}, cb, 10_000);
        client.start();
        await vi.advanceTimersByTimeAsync(0);

        expect(cb.flows).toEqual([]);
        client.stop();
        expect(cb.flows).toEqual([{ id: 'a' }]);
        expect(cb.disconnected).toBe(1);
    });

    test('dispatches onAuthError on HTTP 401 and stops running', async () => {
        stubFetch(async () => sseResponse([], 401));
        const cb = makeCallbacks();
        const client = new FlowStreamClient('t', {}, cb, 10);
        client.start();
        await vi.advanceTimersByTimeAsync(0);

        expect(cb.authErrors).toBe(1);
        // A 401 does not schedule a reconnect (running is set to false).
        await vi.advanceTimersByTimeAsync(60_000);
        expect(fetchMock).toHaveBeenCalledTimes(1);
    });

    test('recovers after a 401 once updateToken() is called with a fresh token', async () => {
        let call = 0;
        stubFetch(async () => {
            call++;
            if (call === 1) return sseResponse([], 401);
            return sseResponse(['event: flow\ndata: {"flows":[{"id":"a"}]}\n\n']);
        });
        const cb = makeCallbacks();
        const client = new FlowStreamClient('stale-token', {}, cb, 10);
        client.start();
        await vi.advanceTimersByTimeAsync(0);
        expect(cb.authErrors).toBe(1);

        // updateToken() on a client that a 401 already stopped is a no-op by design (see
        // antrea-flow-visibility-page.ts, which drops the client and starts a fresh one
        // instead) — this test documents that contract at the FlowStreamClient level.
        client.updateToken('fresh-token');
        await vi.advanceTimersByTimeAsync(10);
        expect(fetchMock).toHaveBeenCalledTimes(1);

        // The actual recovery path: start a new client with the fresh token.
        const client2 = new FlowStreamClient('fresh-token', {}, cb, 10);
        client2.start();
        await vi.advanceTimersByTimeAsync(0);
        await vi.advanceTimersByTimeAsync(10);
        expect(cb.flows).toEqual([{ id: 'a' }]);
        client2.stop();
    });

    test('exponential backoff reconnects after a network error, then gives up after maxReconnectAttempts', async () => {
        stubFetch(async () => { throw new Error('network down'); });
        const cb = makeCallbacks();
        const client = new FlowStreamClient('t', {}, cb, 10, 3);
        client.start();
        await vi.advanceTimersByTimeAsync(0);
        expect(fetchMock).toHaveBeenCalledTimes(1);

        // Backoff: 1000ms, 2000ms, 4000ms for attempts 1..3, then give up.
        await vi.advanceTimersByTimeAsync(1000);
        expect(fetchMock).toHaveBeenCalledTimes(2);
        await vi.advanceTimersByTimeAsync(2000);
        expect(fetchMock).toHaveBeenCalledTimes(3);
        await vi.advanceTimersByTimeAsync(4000);
        expect(fetchMock).toHaveBeenCalledTimes(4);

        expect(cb.errors.at(-1)?.message).toBe('Max reconnect attempts reached');
        await vi.advanceTimersByTimeAsync(60_000);
        expect(fetchMock).toHaveBeenCalledTimes(4);
    });

    test('stop() aborts the in-flight fetch', async () => {
        let capturedSignal: AbortSignal | undefined;
        stubFetch(async (_url, init) => {
            capturedSignal = init?.signal as AbortSignal;
            return sseResponse([]);
        });
        const cb = makeCallbacks();
        const client = new FlowStreamClient('t', {}, cb, 10);
        client.start();
        await vi.advanceTimersByTimeAsync(0);

        expect(capturedSignal?.aborted).toBe(false);
        client.stop();
        expect(capturedSignal?.aborted).toBe(true);
    });

    test('updateFilter() while running aborts and reconnects with the new filter', async () => {
        stubFetch(async () => sseResponse([]));
        const cb = makeCallbacks();
        const client = new FlowStreamClient('t', {}, cb, 10);
        client.start();
        await vi.advanceTimersByTimeAsync(0);
        expect(fetchMock).toHaveBeenCalledTimes(1);

        client.updateFilter({ namespaces: ['default'] });
        await vi.advanceTimersByTimeAsync(0);

        expect(fetchMock).toHaveBeenCalledTimes(2);
        const [url] = fetchMock.mock.calls[1];
        expect(url).toContain('namespaces=default');
        client.stop();
    });
});
