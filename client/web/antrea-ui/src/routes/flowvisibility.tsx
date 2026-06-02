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

import { useState, useCallback } from 'react';
import { CdsButton } from '@cds/react/button';
import FlowFilters from '../components/flow-filters';
import FlowList from './flowlist';
import ServiceMap from './servicemap';
import { FlowStreamFilter } from '../api/flow-stream';
import { useFlowStream } from '../api/use-flow-stream';

type ViewMode = 'list' | 'map';

export default function FlowVisibility() {
    const [viewMode, setViewMode] = useState<ViewMode>('list');
    const [paused, setPaused] = useState(false);
    const [filter, setFilter] = useState<FlowStreamFilter>({ follow: true });

    const {
        entries,
        connected,
        error,
        droppedCount,
        evictionWarning,
        clearFlows,
    } = useFlowStream(filter, paused);

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
        <main>
            <div cds-layout="vertical gap:lg">
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
