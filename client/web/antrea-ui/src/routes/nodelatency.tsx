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

import { useState, useEffect, useMemo } from 'react';
import { CdsCard } from '@cds/react/card';
import { CdsButton } from '@cds/react/button';
import { CdsInput } from '@cds/react/input';
import { CdsCheckbox } from '@cds/react/checkbox';
import { CdsDivider } from '@cds/react/divider';
import { NodeLatencyStats, nodeLatencyStatsAPI } from '../api/nodelatency';
import { useAppError } from '../components/errors';
import { WaitForAPIResource } from '../components/progress';
import NodeLatencyStatsSummary from '../components/nodelatency-stats';
import { NodeDetailPanel, ProblemNodesPanel } from '../components/nodelatency-detail';
import NodeLatencyHeatmap from './nodelatency-heatmap';
import { buildModel } from './nodelatency-util';

type ViewMode = 'heatmap' | 'table';

const REFRESH_INTERVAL_MS = 15 * 1000;

// Cap rows rendered in the flat table so a full mesh on a large cluster (up to N*N rows)
// cannot freeze the page; the heatmap and per-node detail panel cover the full data set.
const MAX_TABLE_ROWS = 500;

const latencyProperties = ['Source Node', 'Target Node', 'Target IP', 'Latency (ms)', 'Last Send', 'Last Recv'];

interface LatencyRow {
    sourceNode: string
    targetNode: string
    targetIP: string
    latencyMs: string
    lastSend: string
    lastRecv: string
}

function formatRTT(rttNanoseconds: number | undefined): string {
    if (rttNanoseconds === undefined || rttNanoseconds <= 0) return 'N/A';
    return (rttNanoseconds / 1e6).toFixed(3);
}

function formatTime(t: string | undefined): string {
    if (!t) return 'None';
    return new Date(t).toLocaleString();
}

function flattenStats(stats: NodeLatencyStats[]): LatencyRow[] {
    const rows: LatencyRow[] = [];
    stats.forEach(stat => {
        stat.peerNodeLatencyStats?.forEach(peer => {
            peer.targetIPLatencyStats?.forEach(target => {
                rows.push({
                    sourceNode: stat.metadata.name,
                    targetNode: peer.nodeName,
                    targetIP: target.targetIP,
                    latencyMs: formatRTT(target.lastMeasuredRTTNanoseconds),
                    lastSend: formatTime(target.lastSendTime),
                    lastRecv: formatTime(target.lastRecvTime),
                });
            });
        });
    });
    return rows;
}

function LatencyTable(props: { rows: LatencyRow[] }) {
    if (props.rows.length === 0) {
        return (
            <CdsCard>
                <div cds-layout="vertical gap:md p:md">
                    <p>No Node latency measurements are available.</p>
                    <p cds-text="secondary">
                        Ensure the Antrea <code>NodeLatencyMonitor</code> feature gate is enabled and a
                        NodeLatencyMonitor resource is configured. Measurements may take a moment to appear.
                    </p>
                </div>
            </CdsCard>
        );
    }
    const shown = props.rows.slice(0, MAX_TABLE_ROWS);
    return (
        <CdsCard>
            <div cds-layout="vertical gap:md">
                {props.rows.length > shown.length && (
                    <p cds-text="secondary">
                        Showing the first {shown.length} of {props.rows.length} measurements. Use the
                        heatmap or search a node to inspect the rest.
                    </p>
                )}
                <table cds-table="border:all" cds-text="center body">
                    <thead>
                        <tr>
                            {latencyProperties.map(name => (
                                <th key={name}>{name}</th>
                            ))}
                        </tr>
                    </thead>
                    <tbody>
                        {shown.map((row, idx) => (
                            <tr key={idx}>
                                <td>{row.sourceNode}</td>
                                <td>{row.targetNode}</td>
                                <td>{row.targetIP}</td>
                                <td>{row.latencyMs}</td>
                                <td>{row.lastSend}</td>
                                <td>{row.lastRecv}</td>
                            </tr>
                        ))}
                    </tbody>
                </table>
            </div>
        </CdsCard>
    );
}

export default function NodeLatency() {
    const [stats, setStats] = useState<NodeLatencyStats[]>();
    const [viewMode, setViewMode] = useState<ViewMode>('heatmap');
    const [selected, setSelected] = useState('');
    const [problemOnly, setProblemOnly] = useState(false);
    const { addError, removeError } = useAppError();

    useEffect(() => {
        let cancelled = false;

        async function getData() {
            try {
                const stats = await nodeLatencyStatsAPI.fetchAll();
                if (cancelled) return;
                setStats(stats);
                removeError();
            } catch (e) {
                if (e instanceof Error) addError(e);
                console.error(e);
            }
        }

        getData();
        const timer = setInterval(getData, REFRESH_INTERVAL_MS);
        return () => {
            cancelled = true;
            clearInterval(timer);
        };
    }, [addError, removeError]);

    const model = useMemo(() => buildModel(stats ?? []), [stats]);
    const rows = useMemo(() => flattenStats(stats ?? []), [stats]);
    const selectedNode = model.nodes.includes(selected) ? selected : '';

    return (
        <main>
            <div cds-layout="vertical gap:lg">
                <p cds-text="title">Node Latency</p>
                <CdsDivider></CdsDivider>
                <WaitForAPIResource ready={stats !== undefined} text="Loading Node Latency Stats">
                    {rows.length === 0 ? (
                        <LatencyTable rows={rows} />
                    ) : (
                        <div cds-layout="vertical gap:lg">
                            <NodeLatencyStatsSummary agg={model.agg} />

                            <CdsInput>
                                <label>Search Node</label>
                                <input
                                    type="text"
                                    list="nl-node-options"
                                    placeholder="Node name"
                                    value={selected}
                                    onChange={e => setSelected(e.target.value)}
                                />
                            </CdsInput>
                            <datalist id="nl-node-options">
                                {model.nodes.map(n => <option key={n} value={n} />)}
                            </datalist>

                            {selectedNode && (
                                <NodeDetailPanel model={model} node={selectedNode} onClose={() => setSelected('')} />
                            )}

                            <ProblemNodesPanel model={model} onSelect={setSelected} />

                            <div cds-layout="horizontal gap:md align:vertical-center">
                                <div cds-layout="horizontal gap:xs">
                                    <CdsButton
                                        type="button"
                                        action={viewMode === 'heatmap' ? 'solid' : 'outline'}
                                        size="sm"
                                        onClick={() => setViewMode('heatmap')}
                                    >
                                        Heatmap
                                    </CdsButton>
                                    <CdsButton
                                        type="button"
                                        action={viewMode === 'table' ? 'solid' : 'outline'}
                                        size="sm"
                                        onClick={() => setViewMode('table')}
                                    >
                                        Table
                                    </CdsButton>
                                </div>
                                {viewMode === 'heatmap' && model.problemNodes.length > 0 && (
                                    <CdsCheckbox>
                                        <label>Problem nodes only</label>
                                        <input
                                            type="checkbox"
                                            checked={problemOnly}
                                            onChange={e => setProblemOnly(e.target.checked)}
                                        />
                                    </CdsCheckbox>
                                )}
                            </div>

                            {viewMode === 'heatmap'
                                ? <NodeLatencyHeatmap model={model} restrictToProblem={problemOnly} />
                                : <LatencyTable rows={rows} />}
                        </div>
                    )}
                </WaitForAPIResource>
            </div>
        </main>
    );
}
