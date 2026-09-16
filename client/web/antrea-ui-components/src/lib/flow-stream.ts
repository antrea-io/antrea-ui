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

import { Flow } from './flow-types.js';
import { getApiBase } from './api.js';

export type FlowFilterDirection = 'both' | 'from' | 'to';
export type FlowTypeName = 'intra-node' | 'inter-node' | 'to-external' | 'from-external';

/** Peer filters. These narrow the stream; they do not authorize it, and e.g. a namespace outside
 * the stream's scope is legal here — it is how a flow is selected by its far end, not its own.
 * No scope of its own: a caller building one of these has to add a FlowStreamScope to get a
 * FlowStreamFilter the backend will accept, which is the point - see withTemporaryClusterWideScope
 * in antrea-flow-visibility-page.ts, the one place that does it today. */
export interface FlowPeerFilter {
    namespaces?: string[];
    pods?: string[];
    podLabelSelector?: string;
    services?: string[];
    flowTypes?: FlowTypeName[];
    ips?: string[];
    direction?: FlowFilterDirection;
}

/** What a flow stream is authorized against: the Flow Aggregator checks RBAC against this and
 * resolves each endpoint's disclosure tier relative to it. Modeled as a discriminated union,
 * rather than two optional fields, so a filter missing a scope - or naming both - is a compile
 * error instead of a request the backend always answers 400 to. */
export type FlowStreamScope =
    | { observedNamespace: string; clusterWide?: never }
    | { observedNamespace?: never; clusterWide: true };

export type FlowStreamFilter = FlowPeerFilter & FlowStreamScope;

export function streamFilterKey(f: FlowStreamFilter): string {
    const namespaces = [...(f.namespaces ?? [])].sort();
    const pods = [...(f.pods ?? [])].sort();
    const services = [...(f.services ?? [])].sort();
    const flowTypes = [...(f.flowTypes ?? [])].sort();
    const ips = [...(f.ips ?? [])].sort();
    const direction = f.direction && f.direction !== 'both' ? f.direction : 'both';
    // The scope must be part of the key. This is the reconnect predicate, so a scope change that
    // did not alter the key would leave the old stream running while the UI claimed to be showing
    // a different namespace — the one way this feature can silently show wrong data.
    return JSON.stringify({
        namespaces,
        pods,
        podLabelSelector: f.podLabelSelector ?? '',
        services,
        flowTypes,
        ips,
        direction,
        observedNamespace: f.observedNamespace ?? '',
        clusterWide: f.clusterWide ?? false,
    });
}

export interface FlowStreamCallbacks {
    onFlows: (flows: Flow[]) => void;
    onError: (error: Error) => void;
    onDropped?: (droppedCount: number) => void;
    onConnected?: () => void;
    onDisconnected?: () => void;
    /** Called on HTTP 401, i.e. the antrea-ui session is over. There is nothing to retry: the host
     * should log the user out. Note that the Flow Aggregator rejecting the credential is *not*
     * this: it reaches onError with an "unauthenticated" code and never a 401, because it says
     * nothing about the antrea-ui session and must not log the user out of the rest of the UI. */
    onAuthError?: () => void;
    /** Called on HTTP 501, i.e. Flow Aggregator integration is disabled for this deployment. This
     * is a static configuration choice, not a transient failure: there is nothing to retry. */
    onDisabled?: () => void;
    /** Called on HTTP 403, i.e. this user is not permitted to view flow data. Like 501, this is a
     * permanent answer for the session, not a transient failure: there is nothing to retry. */
    onForbidden?: () => void;
}

interface SSEEvent { type: string; data: string; }
interface SSEFlowEvent { flows: Flow[]; }
interface SSEDroppedEvent { droppedCount: number; }
/** The backend's flow-stream error payload (apisv1.FlowStreamErrorEvent). The same shape arrives
 * two ways: as the body of an SSE "error" event once the stream is open, and as the JSON body of
 * an HTTP error response when the failure happens before the backend commits to a 200. `retryable`
 * is what decides between reconnecting and stopping, on either path. */
interface FlowStreamErrorPayload { message: string; code?: string; retryable?: boolean; }

function buildStreamURL(filter: FlowStreamFilter): string {
    const params = new URLSearchParams();
    if (filter.namespaces?.length) params.set('namespaces', filter.namespaces.join(','));
    if (filter.pods?.length) params.set('pods', filter.pods.join(','));
    if (filter.podLabelSelector) params.set('podLabelSelector', filter.podLabelSelector);
    if (filter.services?.length) params.set('services', filter.services.join(','));
    if (filter.flowTypes?.length) params.set('flowTypes', filter.flowTypes.join(','));
    if (filter.ips?.length) params.set('ips', filter.ips.join(','));
    if (filter.direction && filter.direction !== 'both') params.set('direction', filter.direction);
    if (filter.observedNamespace) params.set('observedNamespace', filter.observedNamespace);
    if (filter.clusterWide) params.set('clusterWide', 'true');
    return `${getApiBase()}/api/v1/flows/stream?${params.toString()}`;
}

/**
 * FlowStreamClient manages an SSE connection to the flow stream endpoint.
 *
 * Authentication is the session cookie, which the browser attaches to the SSE fetch itself. The
 * backend keeps the session alive for as long as the stream is attached, and closes the stream if
 * the session ends. On HTTP 401 the session is gone for good: onAuthError() fires and the stream
 * stops for good too. On HTTP 501, Flow Aggregator integration is disabled for this deployment:
 * onDisabled() fires and the stream stops for good, the same way.
 *
 * A retryable failure (see scheduleReconnect) is retried indefinitely with a backoff that caps at
 * 30s, never given up on outright. A reconnect due while the tab is hidden is deferred until it
 * becomes visible again (see dueForReconnect) instead of firing in the background.
 */
export class FlowStreamClient {
    private abortController: AbortController | null = null;
    private batchBuffer: Flow[] = [];
    private batchTimer: ReturnType<typeof setInterval> | null = null;
    private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
    private reconnectAttempts = 0;
    // Set by handleSSEEvent when the backend reports a non-retryable stream error (e.g. FA
    // rejected the credential). Checked once the current connect() attempt ends, so a permanent
    // failure stops the client instead of retrying it every reconnectDelay forever - the backend's
    // 200-then-error-event shape otherwise looks identical to a healthy connection that happened
    // to drop, which is what reconnectAttempts is normally reset for.
    private streamErrorIsPermanent = false;
    private filter: FlowStreamFilter;
    private callbacks: FlowStreamCallbacks;
    private batchIntervalMs: number;
    private running = false;
    // Set while a reconnect is due but the tab is hidden; cleared, and acted on, by
    // onVisibilityChange. See dueForReconnect for why a due reconnect does not just fire.
    private reconnectPendingVisibility = false;

    constructor(
        filter: FlowStreamFilter,
        callbacks: FlowStreamCallbacks,
        batchIntervalMs = 1000,
    ) {
        this.filter = filter;
        this.callbacks = callbacks;
        this.batchIntervalMs = batchIntervalMs;
    }

    start(): void {
        if (this.running) return;
        this.running = true;
        this.reconnectAttempts = 0;
        this.startBatchTimer();
        document.addEventListener('visibilitychange', this.onVisibilityChange);
        this.connect();
    }

    stop(): void {
        this.running = false;
        this.abortController?.abort();
        this.abortController = null;
        this.stopBatchTimer();
        if (this.reconnectTimer) { clearTimeout(this.reconnectTimer); this.reconnectTimer = null; }
        this.reconnectPendingVisibility = false;
        document.removeEventListener('visibilitychange', this.onVisibilityChange);
        this.flushBatch();
        this.callbacks.onDisconnected?.();
    }

    updateFilter(filter: FlowStreamFilter): void {
        this.filter = filter;
        if (this.running) {
            this.abortController?.abort();
            if (this.reconnectTimer) { clearTimeout(this.reconnectTimer); this.reconnectTimer = null; }
            this.flushBatch();
            this.batchBuffer = [];
            this.reconnectAttempts = 0;
            this.connect();
        }
    }

    private startBatchTimer(): void {
        this.batchTimer = setInterval(() => this.flushBatch(), this.batchIntervalMs);
    }

    private stopBatchTimer(): void {
        if (this.batchTimer) { clearInterval(this.batchTimer); this.batchTimer = null; }
    }

    private flushBatch(): void {
        if (this.batchBuffer.length === 0) return;
        const batch = this.batchBuffer;
        this.batchBuffer = [];
        this.callbacks.onFlows(batch);
    }

    /** Stops the client for good, for a failure that retrying cannot fix. Every terminal path goes
     * through here rather than just clearing `running`: the batch timer is an interval, so a path
     * that forgets to stop it leaves it firing for the life of the page. This does flush, so
     * flows already received before the terminal failure still reach onFlows; unlike stop(), it
     * does not fire onDisconnected - the caller does that, since the ordering differs by path. */
    private haltPermanently(): void {
        this.running = false;
        this.stopBatchTimer();
        if (this.reconnectTimer) { clearTimeout(this.reconnectTimer); this.reconnectTimer = null; }
        this.reconnectPendingVisibility = false;
        document.removeEventListener('visibilitychange', this.onVisibilityChange);
        this.flushBatch();
    }

    /** Parses the backend's error payload out of a non-OK response, or returns null if the body is
     * not one (a proxy's HTML error page, a truncated body, an older backend). */
    private static async readErrorPayload(response: Response): Promise<FlowStreamErrorPayload | null> {
        try {
            const payload = await response.json() as FlowStreamErrorPayload;
            return typeof payload?.message === 'string' ? payload : null;
        } catch {
            return null;
        }
    }

    private async connect(): Promise<void> {
        if (!this.running) return;
        // Any call here starts a fresh attempt, so a deferred reconnect waiting on
        // onVisibilityChange (see dueForReconnect) is moot - most importantly when this call
        // came from updateFilter() rather than from that deferral itself: without clearing it,
        // the tab later becoming visible would fire a second, concurrent connect() on top of
        // this one, orphaning it (stop() can only abort the abortController this call is about
        // to overwrite).
        this.reconnectPendingVisibility = false;
        this.abortController = new AbortController();
        this.streamErrorIsPermanent = false;
        const url = buildStreamURL(this.filter);
        try {
            const response = await fetch(url, {
                credentials: 'include',
                headers: { 'Accept': 'text/event-stream' },
                signal: this.abortController.signal,
            });

            if (response.status === 401) {
                this.haltPermanently();
                this.callbacks.onAuthError?.();
                this.callbacks.onDisconnected?.();
                return;
            }

            if (response.status === 501) {
                this.haltPermanently();
                this.callbacks.onDisabled?.();
                this.callbacks.onDisconnected?.();
                return;
            }

            // Handled here rather than falling through to !response.ok, which would drive the
            // exponential-backoff reconnect loop against a permanent answer.
            if (response.status === 403) {
                this.haltPermanently();
                this.callbacks.onForbidden?.();
                this.callbacks.onDisconnected?.();
                return;
            }

            if (!response.ok) {
                // The backend gives Subscribe a brief window to report a synchronous failure
                // before it commits to a 200, so the failures that would otherwise arrive as an
                // SSE "error" event mostly arrive here instead - with the same code/retryable
                // classification in the body. Honour it: without this, a permanent failure (the
                // Flow Aggregator rejecting the credential, a credential this deployment cannot
                // mint) would be retried forever purely because it was reported early enough to
                // be an HTTP status rather than late enough to be an event. A body we cannot
                // parse falls through to the retry path, which is the safer default for an
                // unrecognized failure.
                const payload = await FlowStreamClient.readErrorPayload(response);
                const message = payload?.message ?? `Flow stream: ${response.status} ${response.statusText}`;
                if (payload?.retryable === false) {
                    this.haltPermanently();
                    this.callbacks.onError(new Error(message));
                    this.callbacks.onDisconnected?.();
                    return;
                }
                throw new Error(message);
            }
            if (!response.body) throw new Error('Response body is null');

            this.callbacks.onConnected?.();

            const reader = response.body.getReader();
            const decoder = new TextDecoder();
            let buffer = '';
            while (this.running) {
                const { done, value } = await reader.read();
                if (done) break;
                buffer += decoder.decode(value, { stream: true });
                const { parsed, remaining } = this.parseSSEBuffer(buffer);
                buffer = remaining;
                for (const event of parsed) this.handleSSEEvent(event);
            }
        } catch (err) {
            if (!this.running) return;
            if (err instanceof DOMException && err.name === 'AbortError') return;
            this.callbacks.onError(err instanceof Error ? err : new Error(String(err)));
        }
        this.callbacks.onDisconnected?.();
        if (this.streamErrorIsPermanent) {
            // Same terminal treatment as onAuthError/onDisabled/onForbidden above: retrying
            // would just reproduce the same rejection every reconnectDelay, forever.
            this.haltPermanently();
            return;
        }
        if (this.running) this.scheduleReconnect();
    }

    private parseSSEBuffer(buffer: string): { parsed: SSEEvent[]; remaining: string } {
        const events: SSEEvent[] = [];
        const normalized = buffer.replace(/\r\n/g, '\n');
        const blocks = normalized.split('\n\n');
        const remaining = blocks.pop() ?? '';
        for (const block of blocks) {
            if (!block.trim()) continue;
            let eventType = 'message';
            let data = '';
            let isComment = false;
            for (const line of block.split('\n')) {
                if (line.startsWith(':')) { isComment = true; }
                else if (line.startsWith('event:')) { eventType = line.slice(6).trim(); }
                else if (line.startsWith('data:')) {
                    const value = line.startsWith('data: ') ? line.slice(6) : line.slice(5);
                    data += (data ? '\n' : '') + value;
                }
            }
            if (data) events.push({ type: eventType, data });
            // The backend's keepalive comment (": keepalive\n\n") carries no data: line, so it
            // would otherwise be silently dropped here. Surface it as its own event so
            // handleSSEEvent can treat it as proof the connection is alive - without it, a filter
            // that matches nothing would leave reconnectAttempts never reset, so a connection
            // that later drops backs off at the full 30s delay despite never having failed.
            else if (isComment) events.push({ type: 'comment', data: '' });
        }
        return { parsed: events, remaining };
    }

    private handleSSEEvent(event: SSEEvent): void {
        try {
            if (event.type === 'flow') {
                const payload = JSON.parse(event.data) as SSEFlowEvent;
                if (payload.flows?.length) {
                    // Reset here, not on the 200 that opens the connection: the backend can
                    // return 200 and then fail immediately via an "error" event (see below), so
                    // resetting on the 200 alone made every failure retry at a flat 1s delay
                    // forever instead of backing off. Flows are one signal that the connection is
                    // healthy, but not the only one - a narrow filter can leave the FA ring buffer
                    // empty for a long time without anything being wrong, which is why the
                    // "comment" case below (the backend's periodic keepalive) resets this too.
                    this.reconnectAttempts = 0;
                    this.batchBuffer.push(...payload.flows);
                }
            } else if (event.type === 'comment') {
                this.reconnectAttempts = 0;
            } else if (event.type === 'dropped') {
                const payload = JSON.parse(event.data) as SSEDroppedEvent;
                this.reconnectAttempts = 0;
                this.callbacks.onDropped?.(payload.droppedCount);
            } else if (event.type === 'error') {
                const payload = JSON.parse(event.data) as FlowStreamErrorPayload;
                if (payload.retryable === false) this.streamErrorIsPermanent = true;
                this.callbacks.onError(new Error(payload.message));
            }
        } catch (err) { console.error('Failed to parse SSE event', event, err); }
    }

    // Retries a retryable failure indefinitely, the same way Gmail's own connection handling
    // does: there is no failure count here to give up after, only a delay that grows on each
    // attempt and caps at 30s, so a Flow Aggregator restart or a network blip that outlasts a
    // few attempts is still there to reconnect to whenever it recovers. A failure worth giving
    // up on outright (a rejected credential, FA disabled) never reaches this method - it goes
    // through haltPermanently instead.
    private scheduleReconnect(): void {
        const delay = Math.min(1000 * Math.pow(2, this.reconnectAttempts), 30000);
        this.reconnectAttempts++;
        this.reconnectTimer = setTimeout(() => this.dueForReconnect(), delay);
    }

    // A reconnect due while the tab is hidden does not fire: retrying here would open a new
    // stream - and with it a new session-keepalive call (see RequestAuth.KeepAlive) - purely to
    // serve a page nobody is looking at. That is different from an already-open stream, which is
    // deliberately allowed to keep the session alive in the background; this is about not
    // *initiating* one. onVisibilityChange fires the reconnect immediately once the tab is
    // visible again instead of waiting for whatever the backoff delay happened to be.
    private dueForReconnect(): void {
        if (document.visibilityState === 'hidden') {
            this.reconnectPendingVisibility = true;
            return;
        }
        this.connect();
    }

    private onVisibilityChange = (): void => {
        if (this.reconnectPendingVisibility && document.visibilityState === 'visible') {
            this.reconnectPendingVisibility = false;
            this.connect();
        }
    };
}
