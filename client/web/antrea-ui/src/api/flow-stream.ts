/**
 * Copyright 2026 Antrea Authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import { Flow } from './flow-types';
import { getToken } from './token';
import config from '../config';

const { apiUri } = config;

/** Matches Antrea FlowFilter.direction (FlowFilterDirection in protos). */
export type FlowFilterDirection = 'both' | 'from' | 'to';

export interface FlowStreamFilter {
    namespaces?: string[];
    pods?: string[];
    podLabelSelector?: string;
    services?: string[];
    flowTypes?: number[];
    ips?: string[];
    direction?: FlowFilterDirection;
    follow?: boolean;
}

export interface FlowStreamCallbacks {
    onFlows: (flows: Flow[]) => void;
    onError: (error: Error) => void;
    onDropped?: (droppedCount: number) => void;
    onConnected?: () => void;
    onDisconnected?: () => void;
}

interface SSEFlowEvent {
    flows: Flow[];
}

interface SSEDroppedEvent {
    droppedCount: number;
}

interface SSEErrorEvent {
    message: string;
}

function buildStreamURL(filter: FlowStreamFilter): string {
    const params = new URLSearchParams();
    if (filter.namespaces && filter.namespaces.length > 0) {
        params.set('namespaces', filter.namespaces.join(','));
    }
    if (filter.pods && filter.pods.length > 0) {
        params.set('pods', filter.pods.join(','));
    }
    if (filter.podLabelSelector) {
        params.set('podLabelSelector', filter.podLabelSelector);
    }
    if (filter.services && filter.services.length > 0) {
        params.set('services', filter.services.join(','));
    }
    if (filter.flowTypes && filter.flowTypes.length > 0) {
        params.set('flowTypes', filter.flowTypes.join(','));
    }
    if (filter.ips && filter.ips.length > 0) {
        params.set('ips', filter.ips.join(','));
    }
    if (filter.direction && filter.direction !== 'both') {
        params.set('direction', filter.direction);
    }
    params.set('follow', filter.follow !== false ? 'true' : 'false');
    return `${apiUri}/flows/stream?${params.toString()}`;
}

/**
 * FlowStreamClient manages an SSE connection to the flow stream endpoint.
 * Uses fetch() with ReadableStream to support Authorization headers
 * (EventSource does not support custom headers).
 *
 * Accumulated flow events are batched and delivered via callbacks at a
 * configurable interval to reduce re-renders.
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

    constructor(
        filter: FlowStreamFilter,
        callbacks: FlowStreamCallbacks,
        batchIntervalMs = 1000,
        maxReconnectAttempts = 10,
    ) {
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
        if (this.reconnectTimer) {
            clearTimeout(this.reconnectTimer);
            this.reconnectTimer = null;
        }
        this.flushBatch();
        this.callbacks.onDisconnected?.();
    }

    updateFilter(filter: FlowStreamFilter): void {
        this.filter = filter;
        if (this.running) {
            this.abortController?.abort();
            this.reconnectAttempts = 0;
            this.connect();
        }
    }

    private startBatchTimer(): void {
        this.batchTimer = setInterval(() => {
            this.flushBatch();
        }, this.batchIntervalMs);
    }

    private stopBatchTimer(): void {
        if (this.batchTimer) {
            clearInterval(this.batchTimer);
            this.batchTimer = null;
        }
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
                headers: {
                    'Authorization': `Bearer ${getToken()}`,
                    'Accept': 'text/event-stream',
                },
                signal: this.abortController.signal,
            });

            if (!response.ok) {
                throw new Error(`Flow stream request failed: ${response.status} ${response.statusText}`);
            }

            if (!response.body) {
                throw new Error('Response body is null');
            }

            this.reconnectAttempts = 0;
            this.callbacks.onConnected?.();

            const reader = response.body.getReader();
            const decoder = new TextDecoder();
            let buffer = '';

            while (this.running) {
                const { done, value } = await reader.read();
                if (done) break;

                buffer += decoder.decode(value, { stream: true });
                const events = this.parseSSEBuffer(buffer);
                buffer = events.remaining;

                for (const event of events.parsed) {
                    this.handleSSEEvent(event);
                }
            }
        } catch (err) {
            if (!this.running) return;
            if (err instanceof DOMException && err.name === 'AbortError') return;

            this.callbacks.onError(err instanceof Error ? err : new Error(String(err)));
        }

        this.callbacks.onDisconnected?.();

        if (this.running) {
            this.scheduleReconnect();
        }
    }

    private parseSSEBuffer(buffer: string): { parsed: SSEEvent[]; remaining: string } {
        const events: SSEEvent[] = [];
        const blocks = buffer.split('\n\n');
        const remaining = blocks.pop() ?? '';

        for (const block of blocks) {
            if (!block.trim()) continue;
            let eventType = 'message';
            let data = '';
            for (const line of block.split('\n')) {
                if (line.startsWith('event:')) {
                    eventType = line.slice(6).trim();
                } else if (line.startsWith('data:')) {
                    data += line.slice(5).trim();
                }
            }
            if (data) {
                events.push({ type: eventType, data });
            }
        }

        return { parsed: events, remaining };
    }

    private handleSSEEvent(event: SSEEvent): void {
        try {
            switch (event.type) {
                case 'flow': {
                    const payload = JSON.parse(event.data) as SSEFlowEvent;
                    if (payload.flows && payload.flows.length > 0) {
                        this.batchBuffer.push(...payload.flows);
                    }
                    break;
                }
                case 'dropped': {
                    const payload = JSON.parse(event.data) as SSEDroppedEvent;
                    this.callbacks.onDropped?.(payload.droppedCount);
                    break;
                }
                case 'error': {
                    const payload = JSON.parse(event.data) as SSEErrorEvent;
                    this.callbacks.onError(new Error(payload.message));
                    break;
                }
            }
        } catch (err) {
            console.error('Failed to parse SSE event', event, err);
        }
    }

    private scheduleReconnect(): void {
        if (this.reconnectAttempts >= this.maxReconnectAttempts) {
            this.callbacks.onError(new Error('Max reconnect attempts reached'));
            this.stop();
            return;
        }
        const delay = Math.min(1000 * Math.pow(2, this.reconnectAttempts), 30000);
        this.reconnectAttempts++;
        this.reconnectTimer = setTimeout(() => {
            this.connect();
        }, delay);
    }
}

interface SSEEvent {
    type: string;
    data: string;
}
