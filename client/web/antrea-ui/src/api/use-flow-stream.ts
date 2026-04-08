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

import { useEffect, useRef, useState, useCallback } from 'react';
import { Flow } from './flow-types';
import { FlowStore, FlowEntry } from './flow-store';
import { FlowStreamClient, FlowStreamFilter } from './flow-stream';

export interface UseFlowStreamResult {
    entries: FlowEntry[];
    connected: boolean;
    error: string | null;
    droppedCount: number;
    evictionWarning: boolean;
    clearFlows: () => void;
}

export function useFlowStream(filter: FlowStreamFilter, paused: boolean): UseFlowStreamResult {
    const storeRef = useRef(new FlowStore());
    const clientRef = useRef<FlowStreamClient | null>(null);
    const [entries, setEntries] = useState<FlowEntry[]>([]);
    const [connected, setConnected] = useState(false);
    const [error, setError] = useState<string | null>(null);
    const [droppedCount, setDroppedCount] = useState(0);
    const [evictionWarning, setEvictionWarning] = useState(false);

    const handleFlows = useCallback((flows: Flow[]) => {
        storeRef.current.upsertBatch(flows);
        setEntries(storeRef.current.getAll());
        setEvictionWarning(storeRef.current.hasEvicted());
    }, []);

    const handleError = useCallback((err: Error) => {
        setError(err.message);
    }, []);

    const handleDropped = useCallback((count: number) => {
        setDroppedCount(count);
    }, []);

    const handleConnected = useCallback(() => {
        setConnected(true);
        setError(null);
    }, []);

    const handleDisconnected = useCallback(() => {
        setConnected(false);
    }, []);

    const clearFlows = useCallback(() => {
        storeRef.current.clear();
        setEntries([]);
        setEvictionWarning(false);
        setDroppedCount(0);
    }, []);

    const prevFilterRef = useRef(filter);

    useEffect(() => {
        if (filter !== prevFilterRef.current) {
            prevFilterRef.current = filter;
            storeRef.current.clear();
            setEntries([]);
            setEvictionWarning(false);
            setDroppedCount(0);
        }

        if (paused) {
            clientRef.current?.stop();
            clientRef.current = null;
            return;
        }

        const client = new FlowStreamClient(filter, {
            onFlows: handleFlows,
            onError: handleError,
            onDropped: handleDropped,
            onConnected: handleConnected,
            onDisconnected: handleDisconnected,
        });
        clientRef.current = client;
        client.start();

        return () => {
            client.stop();
        };
    }, [filter, paused, handleFlows, handleError, handleDropped, handleConnected, handleDisconnected]);

    return {
        entries,
        connected,
        error,
        droppedCount,
        evictionWarning,
        clearFlows,
    };
}
