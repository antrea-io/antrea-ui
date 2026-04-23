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

import React, { useState, useMemo, useCallback } from 'react';
import { FlowEntry } from '../api/flow-store';
import {
    FlowType,
    flowTypeLabel,
    getProtocolName,
    formatEndpoint,
    formatPolicyInfo,
    formatBytes,
    destinationK8sServiceFilterKey,
} from '../api/flow-types';

type SortField =
    | 'lastSeen'
    | 'source'
    | 'destination'
    | 'destinationService'
    | 'protocol'
    | 'destPort'
    | 'bytesFwd'
    | 'bytesRev'
    | 'ingressPolicy'
    | 'egressPolicy'
    | 'tcpState'
    | 'flowType';

type SortDirection = 'asc' | 'desc';

function formatTimestamp(ts: string): string {
    try {
        const d = new Date(ts);
        return d.toLocaleTimeString();
    } catch {
        return ts;
    }
}

function getSortValue(entry: FlowEntry, field: SortField): string | number {
    const { flow } = entry;
    switch (field) {
        case 'lastSeen':
            return new Date(flow.endTs).getTime();
        case 'source':
            return formatEndpoint(flow.k8s.sourcePodNamespace, flow.k8s.sourcePodName, flow.ip.source);
        case 'destination':
            return formatEndpoint(flow.k8s.destinationPodNamespace, flow.k8s.destinationPodName, flow.ip.destination);
        case 'destinationService':
            return destinationK8sServiceFilterKey(flow.k8s.destinationServicePortName)
                || flow.k8s.destinationServicePortName;
        case 'protocol':
            return flow.transport.protocolNumber;
        case 'destPort':
            return flow.transport.destinationPort;
        case 'bytesFwd':
            return flow.stats.octetTotalCount;
        case 'bytesRev':
            return flow.reverseStats.octetTotalCount;
        case 'ingressPolicy':
            return flow.k8s.ingressNetworkPolicyName;
        case 'egressPolicy':
            return flow.k8s.egressNetworkPolicyName;
        case 'tcpState':
            return flow.transport.tcp?.stateName ?? '';
        case 'flowType':
            return flow.k8s.flowType;
    }
}

function matchesTextFilter(entry: FlowEntry, filterText: string): boolean {
    if (!filterText) return true;
    const lower = filterText.toLowerCase();
    const { flow } = entry;
    const searchable = [
        flow.k8s.sourcePodNamespace,
        flow.k8s.sourcePodName,
        flow.ip.source,
        flow.k8s.destinationPodNamespace,
        flow.k8s.destinationPodName,
        flow.ip.destination,
        flow.k8s.destinationServicePortName,
        destinationK8sServiceFilterKey(flow.k8s.destinationServicePortName),
        getProtocolName(flow.transport.protocolNumber),
        flow.transport.destinationPort.toString(),
        flow.transport.tcp?.stateName ?? '',
        flowTypeLabel[flow.k8s.flowType as FlowType] ?? '',
        flow.k8s.ingressNetworkPolicyName,
        flow.k8s.egressNetworkPolicyName,
    ];
    return searchable.some(s => s.toLowerCase().includes(lower));
}

interface FlowListProps {
    entries: FlowEntry[];
}

const FlowListRow = React.memo(function FlowListRow({ entry }: { entry: FlowEntry }) {
    const { flow } = entry;
    return (
        <tr>
            <td>{formatTimestamp(flow.endTs)}</td>
            <td>{formatEndpoint(flow.k8s.sourcePodNamespace, flow.k8s.sourcePodName, flow.ip.source)}</td>
            <td>{formatEndpoint(flow.k8s.destinationPodNamespace, flow.k8s.destinationPodName, flow.ip.destination)}</td>
            <td
                title={flow.k8s.destinationServicePortName || undefined}
            >
                {destinationK8sServiceFilterKey(flow.k8s.destinationServicePortName)
                    || flow.k8s.destinationServicePortName
                    || '-'}
            </td>
            <td>{getProtocolName(flow.transport.protocolNumber)}</td>
            <td>{flow.transport.destinationPort}</td>
            <td>{formatBytes(flow.stats.octetTotalCount)}</td>
            <td>{formatBytes(flow.reverseStats.octetTotalCount)}</td>
            <td>{formatPolicyInfo(flow.k8s.ingressNetworkPolicyName, flow.k8s.ingressNetworkPolicyRuleAction) || '-'}</td>
            <td>{formatPolicyInfo(flow.k8s.egressNetworkPolicyName, flow.k8s.egressNetworkPolicyRuleAction) || '-'}</td>
            <td>{flow.transport.tcp?.stateName || '-'}</td>
            <td>{flowTypeLabel[flow.k8s.flowType as FlowType] ?? 'Unknown'}</td>
        </tr>
    );
});

const columns: { field: SortField; label: string }[] = [
    { field: 'lastSeen', label: 'Last Seen' },
    { field: 'source', label: 'Source' },
    { field: 'destination', label: 'Destination' },
    { field: 'destinationService', label: 'Dest Service' },
    { field: 'protocol', label: 'Protocol' },
    { field: 'destPort', label: 'Dest Port' },
    { field: 'bytesFwd', label: 'Bytes (Fwd)' },
    { field: 'bytesRev', label: 'Bytes (Rev)' },
    { field: 'ingressPolicy', label: 'Ingress Policy' },
    { field: 'egressPolicy', label: 'Egress Policy' },
    { field: 'tcpState', label: 'TCP State' },
    { field: 'flowType', label: 'Flow Type' },
];

export default function FlowList({ entries }: FlowListProps) {
    const [sortField, setSortField] = useState<SortField>('lastSeen');
    const [sortDir, setSortDir] = useState<SortDirection>('desc');
    const [textFilter, setTextFilter] = useState('');

    const handleSort = useCallback((field: SortField) => {
        setSortField(prev => {
            if (prev === field) {
                setSortDir(d => d === 'asc' ? 'desc' : 'asc');
                return prev;
            }
            setSortDir('asc');
            return field;
        });
    }, []);

    const sortedEntries = useMemo(() => {
        let filtered = entries;
        if (textFilter) {
            filtered = entries.filter(e => matchesTextFilter(e, textFilter));
        }

        const sorted = [...filtered].sort((a, b) => {
            const aVal = getSortValue(a, sortField);
            const bVal = getSortValue(b, sortField);
            let cmp: number;
            if (typeof aVal === 'number' && typeof bVal === 'number') {
                cmp = aVal - bVal;
            } else {
                cmp = String(aVal).localeCompare(String(bVal));
            }
            return sortDir === 'asc' ? cmp : -cmp;
        });

        return sorted;
    }, [entries, sortField, sortDir, textFilter]);

    const sortIndicator = (field: SortField) => {
        if (sortField !== field) return '';
        return sortDir === 'asc' ? ' ▲' : ' ▼';
    };

    return (
        <div cds-layout="vertical gap:md">
            <div cds-layout="horizontal gap:md align:vertical-center">
                <input
                    type="text"
                    placeholder="Filter flows..."
                    value={textFilter}
                    onChange={e => setTextFilter(e.target.value)}
                    style={{
                        padding: '6px 12px',
                        border: '1px solid var(--cds-alias-object-border-color, #565656)',
                        borderRadius: '3px',
                        background: 'var(--cds-alias-object-container-background, #1b2a32)',
                        color: 'var(--cds-global-typography-color-400, #fff)',
                        minWidth: '300px',
                    }}
                />
                <span cds-text="secondary">{sortedEntries.length} connections</span>
            </div>
            <div style={{ maxHeight: '70vh', overflow: 'auto' }}>
                <table cds-table="border:all" cds-text="center body" style={{ width: '100%' }}>
                    <thead>
                        <tr>
                            {columns.map(col => (
                                <th
                                    key={col.field}
                                    onClick={() => handleSort(col.field)}
                                    style={{ cursor: 'pointer', userSelect: 'none', whiteSpace: 'nowrap' }}
                                >
                                    {col.label}{sortIndicator(col.field)}
                                </th>
                            ))}
                        </tr>
                    </thead>
                    <tbody>
                        {sortedEntries.map(entry => (
                            <FlowListRow key={entry.key} entry={entry} />
                        ))}
                    </tbody>
                </table>
            </div>
        </div>
    );
}
