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

import { useState, useCallback, useEffect, useRef, useMemo, useContext } from 'react';
import { CdsButton } from '@cds/react/button';
import FlowFilters from '../components/flow-filters';
import FlowList from './flowlist';
import ServiceMap from './servicemap';
import { Flow } from '../api/flow-types';
import { FlowStore, FlowEntry } from '../store/flow-store';
import { FlowStreamClient, FlowStreamFilter, streamFilterKey } from '../api/flow-stream';
import SettingsContext from '../components/settings';

const flowVisibilityDisabledMessage =
    'Flow visibility is disabled on this Antrea UI server. Install or upgrade the chart with ' +
    '`--set flowAggregator.enabled=true` and a reachable `flowAggregator.address` (see antrea-ui/hack/deploy-kind.sh).';

type ViewMode = 'list' | 'map';

export default function FlowVisibility() {
    const [viewMode, setViewMode] = useState<ViewMode>('list');
    const [paused, setPaused] = useState(false);
    const [filter, setFilter] = useState<FlowStreamFilter>({});

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

    // Periodically refresh entries so derived stats (e.g. the sliding-window bit rate)
    // stay up-to-date between flow record arrivals.
    useEffect(() => {
        const timer = setInterval(() => {
            if (storeRef.current.size() > 0) {
                setEntries(storeRef.current.getAll());
            }
        }, 5000);
        return () => clearInterval(timer);
    }, []);

    const filterKey = useMemo(() => streamFilterKey(filter), [filter]);
    const filterRef = useRef(filter);
    useEffect(() => { filterRef.current = filter; }, [filter]);

    const prevFilterKeyRef = useRef<string | null>(null);

    useEffect(() => {
        if (prevFilterKeyRef.current !== filterKey) {
            prevFilterKeyRef.current = filterKey;
            storeRef.current.clear();
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

    const handleFilterChange = useCallback((newFilter: FlowStreamFilter) => {
        setFilter(newFilter);
    }, []);

    const handlePauseToggle = useCallback(() => {
        setPaused(p => !p);
    }, []);

    const handleClear = useCallback(() => {
        clearFlows();
    }, [clearFlows]);

    return (
        <main style={{ width: '100%', maxWidth: '100%', overflowX: 'auto' }}>
            <div cds-layout="vertical gap:lg" style={{ width: '100%', maxWidth: '100%' }}>
                <div cds-layout="horizontal gap:md align:vertical-center">
                    <p cds-text="title">Flow Visibility</p>
                    <div cds-layout="horizontal gap:xs">
                        <CdsButton
                            type="button"
                            action={viewMode === 'list' ? 'solid' : 'outline'}
                            size="sm"
                            onClick={() => setViewMode('list')}
                        >
                            Flow List
                        </CdsButton>
                        <CdsButton
                            type="button"
                            action={viewMode === 'map' ? 'solid' : 'outline'}
                            size="sm"
                            onClick={() => setViewMode('map')}
                        >
                            Service Map
                        </CdsButton>
                    </div>
                </div>

                <FlowFilters
                    onFilterChange={handleFilterChange}
                    connected={connected}
                    paused={paused}
                    onPauseToggle={handlePauseToggle}
                    onClear={handleClear}
                    connectionCount={entries.length}
                    droppedCount={droppedCount}
                    evictionWarning={evictionWarning}
                    entries={entries}
                />

                {error && (
                    <div style={{ color: '#c21d00', padding: '8px', border: '1px solid #c21d00', borderRadius: '4px' }}>
                        Error: {error}
                    </div>
                )}

                {viewMode === 'list' ? (
                    <FlowList entries={entries} />
                ) : (
                    <ServiceMap entries={entries} />
                )}
            </div>
        </main>
    );
}
