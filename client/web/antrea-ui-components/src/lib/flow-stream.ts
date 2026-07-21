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

export type FlowFilterDirection = 'both' | 'from' | 'to';
export type FlowTypeName = 'intra-node' | 'inter-node' | 'to-external' | 'from-external';

export interface FlowStreamFilter {
    namespaces?: string[];
    pods?: string[];
    podLabelSelector?: string;
    services?: string[];
    flowTypes?: FlowTypeName[];
    ips?: string[];
    direction?: FlowFilterDirection;
}

export function streamFilterKey(f: FlowStreamFilter): string {
    const namespaces = [...(f.namespaces ?? [])].sort();
    const pods = [...(f.pods ?? [])].sort();
    const services = [...(f.services ?? [])].sort();
    const flowTypes = [...(f.flowTypes ?? [])].sort();
    const ips = [...(f.ips ?? [])].sort();
    const direction = f.direction && f.direction !== 'both' ? f.direction : 'both';
    return JSON.stringify({ namespaces, pods, podLabelSelector: f.podLabelSelector ?? '', services, flowTypes, ips, direction });
}

export interface FlowStreamCallbacks {
    onFlows: (flows: Flow[]) => void;
    onError: (error: Error) => void;
    onDropped?: (droppedCount: number) => void;
    onConnected?: () => void;
    onDisconnected?: () => void;
    /** Called on HTTP 401. Host should refresh the token and call updateToken(). */
    onAuthError?: () => void;
}

interface SSEEvent { type: string; data: string; }
interface SSEFlowEvent { flows: Flow[]; }
interface SSEDroppedEvent { droppedCount: number; }
interface SSEErrorEvent { message: string; }

function buildStreamURL(filter: FlowStreamFilter): string {
    const params = new URLSearchParams();
    if (filter.namespaces?.length) params.set('namespaces', filter.namespaces.join(','));
    if (filter.pods?.length) params.set('pods', filter.pods.join(','));
    if (filter.podLabelSelector) params.set('podLabelSelector', filter.podLabelSelector);
    if (filter.services?.length) params.set('services', filter.services.join(','));
    if (filter.flowTypes?.length) params.set('flowTypes', filter.flowTypes.join(','));
    if (filter.ips?.length) params.set('ips', filter.ips.join(','));
    if (filter.direction && filter.direction !== 'both') params.set('direction', filter.direction);
    return `/api/v1/flows/stream?${params.toString()}`;
}

/**
 * FlowStreamClient manages an SSE connection to the flow stream endpoint.
 * Unlike the React version, the token is passed explicitly (not read from Redux).
 * On HTTP 401, onAuthError() is called and the stream stops; call updateToken()
 * with the new token to reconnect.
 */
export class FlowStreamClient {
    private abortController: AbortController | null = null;
    private batchBuffer: Flow[] = [];
    private batchTimer: ReturnType<typeof setInterval> | null = null;
    private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
    private reconnectAttempts = 0;
    private filter: FlowStreamFilter;
    private callbacks: FlowStreamCallbacks;
    private batchIntervalMs: number;
    private maxReconnectAttempts: number;
    private running = false;
    private token: string;

    constructor(
        token: string,
        filter: FlowStreamFilter,
        callbacks: FlowStreamCallbacks,
        batchIntervalMs = 1000,
        maxReconnectAttempts = 10,
    ) {
        this.token = token;
        this.filter = filter;
        this.callbacks = callbacks;
        this.batchIntervalMs = batchIntervalMs;
        this.maxReconnectAttempts = maxReconnectAttempts;
    }

    start(): void {
        if (this.running) return;
        this.running = true;
        this.reconnectAttempts = 0;
        this.startBatchTimer();
        this.connect();
    }

    stop(): void {
        this.running = false;
        this.abortController?.abort();
        this.abortController = null;
        this.stopBatchTimer();
        if (this.reconnectTimer) { clearTimeout(this.reconnectTimer); this.reconnectTimer = null; }
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

    updateToken(token: string): void {
        this.token = token;
        if (this.running) {
            this.abortController?.abort();
            if (this.reconnectTimer) { clearTimeout(this.reconnectTimer); this.reconnectTimer = null; }
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

    private async connect(): Promise<void> {
        if (!this.running) return;
        this.abortController = new AbortController();
        const url = buildStreamURL(this.filter);
        try {
            const response = await fetch(url, {
                headers: { 'Authorization': `Bearer ${this.token}`, 'Accept': 'text/event-stream' },
                signal: this.abortController.signal,
            });

            if (response.status === 401) {
                this.running = false;
                this.stopBatchTimer();
                this.callbacks.onAuthError?.();
                this.callbacks.onDisconnected?.();
                return;
            }

            if (!response.ok) throw new Error(`Flow stream: ${response.status} ${response.statusText}`);
            if (!response.body) throw new Error('Response body is null');

            this.reconnectAttempts = 0;
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
            for (const line of block.split('\n')) {
                if (line.startsWith('event:')) { eventType = line.slice(6).trim(); }
                else if (line.startsWith('data:')) {
                    const value = line.startsWith('data: ') ? line.slice(6) : line.slice(5);
                    data += (data ? '\n' : '') + value;
                }
            }
            if (data) events.push({ type: eventType, data });
        }
        return { parsed: events, remaining };
    }

    private handleSSEEvent(event: SSEEvent): void {
        try {
            if (event.type === 'flow') {
                const payload = JSON.parse(event.data) as SSEFlowEvent;
                if (payload.flows?.length) this.batchBuffer.push(...payload.flows);
            } else if (event.type === 'dropped') {
                const payload = JSON.parse(event.data) as SSEDroppedEvent;
                this.callbacks.onDropped?.(payload.droppedCount);
            } else if (event.type === 'error') {
                const payload = JSON.parse(event.data) as SSEErrorEvent;
                this.callbacks.onError(new Error(payload.message));
            }
        } catch (err) { console.error('Failed to parse SSE event', event, err); }
    }

    private scheduleReconnect(): void {
        if (this.reconnectAttempts >= this.maxReconnectAttempts) {
            this.callbacks.onError(new Error('Max reconnect attempts reached'));
            this.stop();
            return;
        }
        const delay = Math.min(1000 * Math.pow(2, this.reconnectAttempts), 30000);
        this.reconnectAttempts++;
        this.reconnectTimer = setTimeout(() => this.connect(), delay);
    }
}
