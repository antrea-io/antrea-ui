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

import { useEffect, useRef, useState, useCallback, useMemo, useContext } from 'react';
import { Flow } from './flow-types';
import { FlowStore, FlowEntry } from './flow-store';
import { FlowStreamClient, FlowStreamFilter, streamFilterKey } from './flow-stream';
import SettingsContext from '../components/settings';

export interface UseFlowStreamResult {
    entries: FlowEntry[];
    connected: boolean;
    error: string | null;
    droppedCount: number;
    evictionWarning: boolean;
    clearFlows: () => void;
}

const flowVisibilityDisabledMessage =
    'Flow visibility is disabled on this Antrea UI server. Install or upgrade the chart with ' +
    '`--set flowAggregator.enabled=true` and a reachable `flowAggregator.address` (see antrea-ui/hack/deploy-kind.sh).';

export function useFlowStream(filter: FlowStreamFilter, paused: boolean): UseFlowStreamResult {
    const settings = useContext(SettingsContext);
    const flowVisibilityOff = settings.features?.flowVisibilityEnabled === false;

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

    const filterKey = useMemo(() => streamFilterKey(filter), [filter]);
    // Store filter in a ref so we can use the latest filter when instantiating the client
    // without triggering effect re-runs on every filter object identity change.
    const filterRef = useRef(filter);
    useEffect(() => { filterRef.current = filter; }, [filter]);

    const prevFilterKeyRef = useRef<string | null>(null);

    useEffect(() => {
        if (prevFilterKeyRef.current !== filterKey) {
            prevFilterKeyRef.current = filterKey;
            storeRef.current.clear();
            // Stop and discard the old client so a new one is created below.
            clientRef.current?.stop();
            clientRef.current = null;
            setTimeout(() => {
                setEntries([]);
                setEvictionWarning(false);
                setDroppedCount(0);
            }, 0);
        }

        if (flowVisibilityOff) {
            clientRef.current?.stop();
            clientRef.current = null;
            const timer = setTimeout(() => {
                setConnected(false);
                setError(flowVisibilityDisabledMessage);
            }, 0);
            return () => clearTimeout(timer);
        }

        if (paused) {
            clientRef.current?.stop();
            clientRef.current = null;
            return;
        }

        const client = new FlowStreamClient(filterRef.current, {
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
    }, [
        filterKey,
        paused,
        flowVisibilityOff,
        handleFlows,
        handleError,
        handleDropped,
        handleConnected,
        handleDisconnected,
    ]);

    return {
        entries,
        connected,
        error,
        droppedCount,
        evictionWarning,
        clearFlows,
    };
}
