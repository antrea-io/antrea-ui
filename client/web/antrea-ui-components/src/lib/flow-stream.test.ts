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

    // Unlike sseResponse, closes the stream after all chunks: needed for tests that check what
    // happens once a connection attempt ends (reconnect scheduling), since a stream left open
    // never lets connect()'s read loop finish.
    function closingSseResponse(chunks: string[], status = 200): Response {
        const encoder = new TextEncoder();
        const stream = new ReadableStream<Uint8Array>({
            start(controller) {
                for (const chunk of chunks) controller.enqueue(encoder.encode(chunk));
                controller.close();
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
        disabled: number;
    } {
        const cb = {
            flows: [] as unknown[],
            errors: [] as Error[],
            dropped: [] as number[],
            connected: 0,
            disconnected: 0,
            authErrors: 0,
            disabled: 0,
            forbidden: 0,
            onFlows: (flows: unknown[]) => { cb.flows.push(...flows); },
            onError: (err: Error) => { cb.errors.push(err); },
            onDropped: (count: number) => { cb.dropped.push(count); },
            onConnected: () => { cb.connected++; },
            onDisconnected: () => { cb.disconnected++; },
            onAuthError: () => { cb.authErrors++; },
            onDisabled: () => { cb.disabled++; },
            onForbidden: () => { cb.forbidden++; },
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

    // The SSE fetch authenticates with the session cookie, so it must opt into sending
    // credentials (local development runs the frontend on a different origin) and must not send
    // an Authorization header of its own.
    test('sends the session cookie and connects', async () => {
        stubFetch(async () => sseResponse([]));
        const cb = makeCallbacks();
        const client = new FlowStreamClient({}, cb);

        client.start();
        await vi.advanceTimersByTimeAsync(0);

        expect(fetchMock).toHaveBeenCalledTimes(1);
        const [url, init] = fetchMock.mock.calls[0];
        expect(url).toContain('/api/v1/flows/stream');
        expect(init.credentials).toBe('include');
        expect((init.headers as Record<string, string>).Authorization).toBeUndefined();
        expect(cb.connected).toBe(1);

        client.stop();
    });

    test('prefixes the stream URL with the configured API base', async () => {
        setApiBase('http://localhost:8080');
        stubFetch(async () => sseResponse([]));
        const cb = makeCallbacks();
        const client = new FlowStreamClient({}, cb);

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
        const client = new FlowStreamClient({}, cb, 10);
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
        const client = new FlowStreamClient({}, cb, 10);
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
        const client = new FlowStreamClient({}, cb, 10);
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
        const client = new FlowStreamClient({}, cb, 10);
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
        const client = new FlowStreamClient({}, cb, 50);
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
        const client = new FlowStreamClient({}, cb, 10_000);
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
        const client = new FlowStreamClient({}, cb, 10);
        client.start();
        await vi.advanceTimersByTimeAsync(0);

        expect(cb.authErrors).toBe(1);
        // A 401 does not schedule a reconnect (running is set to false).
        await vi.advanceTimersByTimeAsync(60_000);
        expect(fetchMock).toHaveBeenCalledTimes(1);
    });

    // A 401 is terminal: there is no token left to refresh, so the client stops for good rather
    // than hammering a session that is gone. The host logs the user out in response.
    test('a 401 stops the client and nothing restarts it on its own', async () => {
        stubFetch(async () => sseResponse([], 401));
        const cb = makeCallbacks();
        const client = new FlowStreamClient({}, cb, 10);
        client.start();
        await vi.advanceTimersByTimeAsync(0);
        expect(cb.authErrors).toBe(1);

        await vi.advanceTimersByTimeAsync(60_000);
        expect(fetchMock).toHaveBeenCalledTimes(1);
    });

    // A 501 means Flow Aggregator integration is off for this deployment — a static config
    // choice, not a transient failure — so it is terminal, the same way a 401 is.
    test('dispatches onDisabled on HTTP 501 and does not reconnect', async () => {
        stubFetch(async () => sseResponse([], 501));
        const cb = makeCallbacks();
        const client = new FlowStreamClient({}, cb, 10);
        client.start();
        await vi.advanceTimersByTimeAsync(0);

        expect(cb.disabled).toBe(1);
        await vi.advanceTimersByTimeAsync(60_000);
        expect(fetchMock).toHaveBeenCalledTimes(1);
    });

    // A 403 means this user may not view flow data. Like a 501 it is a permanent answer for the
    // session, so it must not fall through to !response.ok and drive the backoff loop against it.
    test('dispatches onForbidden on HTTP 403 and does not reconnect', async () => {
        stubFetch(async () => sseResponse([], 403));
        const cb = makeCallbacks();
        const client = new FlowStreamClient({}, cb, 10);
        client.start();
        await vi.advanceTimersByTimeAsync(0);

        expect(cb.forbidden).toBe(1);
        expect(cb.errors).toHaveLength(0);
        await vi.advanceTimersByTimeAsync(60_000);
        expect(fetchMock).toHaveBeenCalledTimes(1);
    });

    test('exponential backoff reconnects after a network error, then gives up after maxReconnectAttempts', async () => {
        stubFetch(async () => { throw new Error('network down'); });
        const cb = makeCallbacks();
        const client = new FlowStreamClient({}, cb, 10, 3);
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

    // A stream "error" event with retryable:false (e.g. FA rejected the credential) is a
    // permanent answer for this connection, not a transient failure: the client must not keep
    // reconnecting into the same rejection every reconnectDelay forever.
    test('a non-retryable stream error event stops the client without reconnecting', async () => {
        stubFetch(async () => closingSseResponse([
            'event: error\ndata: {"message":"rejected","code":"unauthenticated","retryable":false}\n\n',
        ]));
        const cb = makeCallbacks();
        const client = new FlowStreamClient({}, cb, 10);
        client.start();
        await vi.advanceTimersByTimeAsync(0);

        expect(cb.errors.map(e => e.message)).toEqual(['rejected']);
        await vi.advanceTimersByTimeAsync(60_000);
        expect(fetchMock).toHaveBeenCalledTimes(1);
    });

    // The batch timer is an interval, so a terminal path that only clears `running` leaves it
    // firing for the life of the page. Nothing else observes it, which is exactly why it is
    // asserted here: after a permanent stop, no timer of ours may be left pending.
    test('a non-retryable stream error event leaves no timer behind', async () => {
        stubFetch(async () => closingSseResponse([
            'event: error\ndata: {"message":"rejected","code":"unauthenticated","retryable":false}\n\n',
        ]));
        const cb = makeCallbacks();
        const client = new FlowStreamClient({}, cb, 10);
        client.start();
        await vi.advanceTimersByTimeAsync(0);

        expect(vi.getTimerCount()).toBe(0);
    });

    // The backend peeks for a synchronous failure before committing to a 200 (see StreamFlows's
    // errorPeekTimeout), so the failures that would otherwise arrive as an SSE "error" event
    // mostly arrive as an HTTP error status carrying the same code/retryable body. A permanent one
    // must be just as terminal on that path - otherwise a rejected credential gets retried the
    // full maxReconnectAttempts times purely because it was reported early rather than late.
    test('a non-retryable pre-200 error body stops the client without reconnecting', async () => {
        stubFetch(async () => new Response(
            JSON.stringify({ message: 'FlowAggregator rejected the credential', code: 'unauthenticated', retryable: false }),
            { status: 502, headers: { 'Content-Type': 'application/json' } },
        ));
        const cb = makeCallbacks();
        const client = new FlowStreamClient({}, cb, 10);
        client.start();
        await vi.advanceTimersByTimeAsync(0);

        expect(cb.errors.map(e => e.message)).toEqual(['FlowAggregator rejected the credential']);
        // Emphatically not onAuthError: FA rejecting the credential says nothing about the
        // antrea-ui session, and the host logs the user out when that fires.
        expect(cb.authErrors).toBe(0);
        await vi.advanceTimersByTimeAsync(60_000);
        expect(fetchMock).toHaveBeenCalledTimes(1);
        expect(vi.getTimerCount()).toBe(0);
    });

    // The retryable half of the same path: FA at capacity before the 200 is a 503, and capacity is
    // expected to free up, so this one goes back through the normal backoff.
    test('a retryable pre-200 error body reconnects with backoff', async () => {
        let calls = 0;
        stubFetch(async () => {
            calls++;
            return new Response(
                JSON.stringify({ message: 'at capacity', code: 'resource_exhausted', retryable: true }),
                { status: 503, headers: { 'Content-Type': 'application/json' } },
            );
        });
        const cb = makeCallbacks();
        const client = new FlowStreamClient({}, cb, 10);
        client.start();
        await vi.advanceTimersByTimeAsync(0);
        expect(calls).toBe(1);
        expect(cb.errors.map(e => e.message)).toEqual(['at capacity']);

        await vi.advanceTimersByTimeAsync(1000);
        expect(calls).toBe(2);
        client.stop();
    });

    // An error status whose body is not ours at all - an nginx HTML error page, a body that never
    // arrived - must fall through to the retry path rather than being read as permanent, and the
    // reported message has to fall back to the status line so the user sees something.
    test('an unparseable error body falls back to the status line and reconnects', async () => {
        let calls = 0;
        stubFetch(async () => {
            calls++;
            return new Response('<html>502 Bad Gateway</html>', { status: 502 });
        });
        const cb = makeCallbacks();
        const client = new FlowStreamClient({}, cb, 10);
        client.start();
        await vi.advanceTimersByTimeAsync(0);
        expect(cb.errors[0].message).toContain('502');

        await vi.advanceTimersByTimeAsync(1000);
        expect(calls).toBe(2);
        client.stop();
    });

    // A stream "error" event with retryable:true (e.g. FA at capacity) must still reconnect with
    // the normal exponential backoff.
    test('a retryable stream error event still reconnects', async () => {
        let calls = 0;
        stubFetch(async () => {
            calls++;
            return closingSseResponse([
                'event: error\ndata: {"message":"at capacity","code":"resource_exhausted","retryable":true}\n\n',
            ]);
        });
        const cb = makeCallbacks();
        const client = new FlowStreamClient({}, cb, 10);
        client.start();
        await vi.advanceTimersByTimeAsync(0);
        expect(calls).toBe(1);

        await vi.advanceTimersByTimeAsync(1000);
        expect(calls).toBe(2);
        client.stop();
    });

    // reconnectAttempts must reset on actual data flowing, not on the HTTP 200 that opens the
    // connection: the backend can return 200 and then fail immediately via an "error" event, so
    // resetting on the 200 alone would keep every retry at the flat first backoff step forever
    // instead of growing it.
    test('reconnectAttempts resets on a flow event, not on the 200 that opens the connection', async () => {
        let call = 0;
        stubFetch(async () => {
            call++;
            // Every connection attempt returns 200 and then an immediate retryable error, with
            // no flow data ever received.
            return closingSseResponse([
                'event: error\ndata: {"message":"at capacity","code":"resource_exhausted","retryable":true}\n\n',
            ]);
        });
        const cb = makeCallbacks();
        const client = new FlowStreamClient({}, cb, 10);
        client.start();
        await vi.advanceTimersByTimeAsync(0);
        expect(call).toBe(1);

        // If reconnectAttempts were reset on the 200, this would still be a 1000ms backoff
        // instead of growing to 2000ms.
        await vi.advanceTimersByTimeAsync(1000);
        expect(call).toBe(2);
        await vi.advanceTimersByTimeAsync(1000);
        expect(call).toBe(2); // Not yet: backoff grew to 2000ms.
        await vi.advanceTimersByTimeAsync(1000);
        expect(call).toBe(3);
        client.stop();
    });

    test('stop() aborts the in-flight fetch', async () => {
        let capturedSignal: AbortSignal | undefined;
        stubFetch(async (_url, init) => {
            capturedSignal = init?.signal as AbortSignal;
            return sseResponse([]);
        });
        const cb = makeCallbacks();
        const client = new FlowStreamClient({}, cb, 10);
        client.start();
        await vi.advanceTimersByTimeAsync(0);

        expect(capturedSignal?.aborted).toBe(false);
        client.stop();
        expect(capturedSignal?.aborted).toBe(true);
    });

    test('updateFilter() while running aborts and reconnects with the new filter', async () => {
        stubFetch(async () => sseResponse([]));
        const cb = makeCallbacks();
        const client = new FlowStreamClient({}, cb, 10);
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
