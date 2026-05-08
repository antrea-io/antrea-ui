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

import { useState, useCallback, useMemo, useRef, useEffect } from 'react';
import { CdsButton } from '@cds/react/button';
import { CdsSelect } from '@cds/react/select';
import { FlowType, flowTypeLabel, destinationK8sServiceFilterKey } from '../api/flow-types';
import { FlowStreamFilter, FlowFilterDirection } from '../api/flow-stream';
import { FlowEntry } from '../api/flow-store';

interface FlowFiltersProps {
    onFilterChange: (filter: FlowStreamFilter) => void;
    connected: boolean;
    paused: boolean;
    onPauseToggle: () => void;
    onClear: () => void;
    connectionCount: number;
    droppedCount: number;
    evictionWarning: boolean;
    entries: FlowEntry[];
}

function MultiSelect({ label, options, selected, onChange, closeSignal }: {
    label: string;
    options: string[];
    selected: string[];
    onChange: (values: string[]) => void;
    /** Increment after Apply Filters to force-close the dropdown. */
    closeSignal?: number;
}) {
    const [open, setOpen] = useState(false);
    const ref = useRef<HTMLDivElement>(null);

    useEffect(() => {
        function handleClickOutside(e: MouseEvent) {
            if (ref.current && !ref.current.contains(e.target as Node)) {
                setOpen(false);
            }
        }
        document.addEventListener('mousedown', handleClickOutside);
        return () => document.removeEventListener('mousedown', handleClickOutside);
    }, []);

    useEffect(() => {
        if (closeSignal !== undefined && closeSignal > 0) {
            // eslint-disable-next-line react-hooks/set-state-in-effect
            setOpen(false);
        }
    }, [closeSignal]);

    // After Apply Filters the stream clears entries briefly; options can become empty while
    // `open` is still true, which blocks sensible interaction. Close the panel when there is
    // nothing to show.
    useEffect(() => {
        if (open && options.length === 0) {
            // eslint-disable-next-line react-hooks/set-state-in-effect
            setOpen(false);
        }
    }, [open, options.length]);

    const toggle = (value: string) => {
        if (selected.includes(value)) {
            onChange(selected.filter(v => v !== value));
        } else {
            onChange([...selected, value]);
        }
    };

    const displayText = selected.length === 0
        ? 'All'
        : selected.length <= 2
            ? selected.join(', ')
            : `${selected.slice(0, 2).join(', ')} +${selected.length - 2}`;

    return (
        <div ref={ref} style={{ position: 'relative', minWidth: '180px' }}>
            <label style={{
                display: 'block',
                fontSize: '11px',
                color: 'var(--cds-global-typography-color-300, #adbbc4)',
                marginBottom: '4px',
            }}>{label}</label>
            <button
                type="button"
                onClick={() => setOpen(!open)}
                style={{
                    width: '100%',
                    padding: '6px 28px 6px 10px',
                    background: 'var(--cds-alias-object-interaction-background, #1b2a32)',
                    border: '1px solid var(--cds-alias-object-border-color, #565656)',
                    borderRadius: '3px',
                    color: 'var(--cds-global-typography-color-400, #e0e8ec)',
                    fontSize: '13px',
                    textAlign: 'left',
                    cursor: 'pointer',
                    position: 'relative',
                    whiteSpace: 'nowrap',
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                }}
            >
                {displayText}
                <span style={{
                    position: 'absolute',
                    right: '8px',
                    top: '50%',
                    transform: 'translateY(-50%)',
                    fontSize: '10px',
                    pointerEvents: 'none',
                }}>&#9662;</span>
            </button>
            {open && options.length > 0 && (
                <div style={{
                    position: 'absolute',
                    top: '100%',
                    left: 0,
                    right: 0,
                    maxHeight: '200px',
                    overflowY: 'auto',
                    background: 'var(--cds-alias-object-container-background, #1b2a32)',
                    border: '1px solid var(--cds-alias-object-border-color, #565656)',
                    borderRadius: '0 0 3px 3px',
                    zIndex: 50,
                }}>
                    {options.map(opt => (
                        <label
                            key={opt}
                            style={{
                                display: 'flex',
                                alignItems: 'center',
                                gap: '8px',
                                padding: '5px 10px',
                                cursor: 'pointer',
                                fontSize: '12px',
                                color: 'var(--cds-global-typography-color-400, #e0e8ec)',
                                background: selected.includes(opt) ? 'rgba(106,159,181,0.15)' : 'transparent',
                            }}
                        >
                            <input
                                type="checkbox"
                                checked={selected.includes(opt)}
                                onChange={() => toggle(opt)}
                                style={{ accentColor: '#6a9fb5' }}
                            />
                            {opt}
                        </label>
                    ))}
                </div>
            )}
        </div>
    );
}

export default function FlowFilters({
    onFilterChange,
    connected,
    paused,
    onPauseToggle,
    onClear,
    connectionCount,
    droppedCount,
    evictionWarning,
    entries,
}: FlowFiltersProps) {
    const [selectedNamespaces, setSelectedNamespaces] = useState<string[]>([]);
    const [selectedPods, setSelectedPods] = useState<string[]>([]);
    const [selectedServices, setSelectedServices] = useState<string[]>([]);
    const [flowType, setFlowType] = useState<string>('');
    const [direction, setDirection] = useState<FlowFilterDirection>('both');
    const [ipsText, setIpsText] = useState('');
    const [podLabelSelector, setPodLabelSelector] = useState('');
    const [menusCloseNonce, setMenusCloseNonce] = useState(0);

    const availableNamespaces = useMemo(() => {
        const ns = new Set<string>();
        for (const e of entries) {
            if (e.flow.k8s.sourcePodNamespace) ns.add(e.flow.k8s.sourcePodNamespace);
            if (e.flow.k8s.destinationPodNamespace) ns.add(e.flow.k8s.destinationPodNamespace);
        }
        return Array.from(ns).sort();
    }, [entries]);

    const availablePods = useMemo(() => {
        const pods = new Set<string>();
        for (const e of entries) {
            if (e.flow.k8s.sourcePodName) pods.add(e.flow.k8s.sourcePodName);
            if (e.flow.k8s.destinationPodName) pods.add(e.flow.k8s.destinationPodName);
        }
        return Array.from(pods).sort();
    }, [entries]);

    const availableServices = useMemo(() => {
        const svcs = new Set<string>();
        for (const e of entries) {
            const key = destinationK8sServiceFilterKey(e.flow.k8s.destinationServicePortName);
            if (key) {
                svcs.add(key);
            }
        }
        return Array.from(svcs).sort();
    }, [entries]);

    // Drop selections that are not real service names (e.g. legacy "http" from port-name-only UI).
    useEffect(() => {
        if (availableServices.length === 0) {
            return;
        }
        const valid = new Set(availableServices);
        // eslint-disable-next-line react-hooks/set-state-in-effect
        setSelectedServices(prev => {
            const next = prev.filter(s => valid.has(s));
            return next.length === prev.length ? prev : next;
        });
    }, [availableServices]);

    const applyFilters = useCallback(() => {
        setMenusCloseNonce(n => n + 1);
        const filter: FlowStreamFilter = { follow: true };
        if (selectedNamespaces.length > 0) {
            filter.namespaces = selectedNamespaces;
        }
        if (selectedPods.length > 0) {
            filter.pods = selectedPods;
        }
        if (podLabelSelector.trim()) {
            filter.podLabelSelector = podLabelSelector.trim();
        }
        if (selectedServices.length > 0) {
            const names = new Set<string>();
            for (const s of selectedServices) {
                const k = destinationK8sServiceFilterKey(s);
                if (k) {
                    names.add(k);
                }
            }
            if (names.size > 0) {
                filter.services = Array.from(names);
            }
        }
        if (flowType) {
            filter.flowTypes = [parseInt(flowType)];
        }
        const parsedIPs = ipsText.split(',').map(s => s.trim()).filter(Boolean);
        if (parsedIPs.length > 0) {
            filter.ips = parsedIPs;
        }
        if (direction !== 'both') {
            filter.direction = direction;
        }
        onFilterChange(filter);
    }, [selectedNamespaces, selectedPods, selectedServices, flowType, direction, ipsText, podLabelSelector, onFilterChange]);

    const statusColor = connected ? '#60b515' : '#c21d00';
    const statusText = connected ? 'Connected' : (paused ? 'Paused' : 'Disconnected');

    const flowTypeOptions = useMemo(() => [
        { value: '', label: 'All' },
        { value: String(FlowType.IntraNode), label: flowTypeLabel[FlowType.IntraNode] },
        { value: String(FlowType.InterNode), label: flowTypeLabel[FlowType.InterNode] },
        { value: String(FlowType.ToExternal), label: flowTypeLabel[FlowType.ToExternal] },
        { value: String(FlowType.FromExternal), label: flowTypeLabel[FlowType.FromExternal] },
    ], []);

    return (
        <div cds-layout="vertical gap:md">
            <div cds-layout="horizontal gap:lg align:vertical-center wrap:wrap">
                <div style={{ display: 'flex', gap: '16px', flexWrap: 'wrap', alignItems: 'flex-end' }}>
                    <MultiSelect
                        label="Namespaces"
                        options={availableNamespaces}
                        selected={selectedNamespaces}
                        onChange={setSelectedNamespaces}
                        closeSignal={menusCloseNonce}
                    />
                    <MultiSelect
                        label="Pod Names"
                        options={availablePods}
                        selected={selectedPods}
                        onChange={setSelectedPods}
                        closeSignal={menusCloseNonce}
                    />
                    <MultiSelect
                        label="Service Names"
                        options={availableServices}
                        selected={selectedServices}
                        onChange={setSelectedServices}
                        closeSignal={menusCloseNonce}
                    />
                    <div style={{ minWidth: '140px' }}>
                        <CdsSelect>
                            <label>Flow Type</label>
                            <select value={flowType} onChange={e => setFlowType(e.target.value)}>
                                {flowTypeOptions.map(opt => (
                                    <option key={opt.value} value={opt.value}>{opt.label}</option>
                                ))}
                            </select>
                        </CdsSelect>
                    </div>
                    <div style={{ minWidth: '120px' }}>
                        <CdsSelect>
                            <label>Direction</label>
                            <select value={direction} onChange={e => setDirection(e.target.value as FlowFilterDirection)}>
                                <option value="both">Both</option>
                                <option value="from">From</option>
                                <option value="to">To</option>
                            </select>
                        </CdsSelect>
                    </div>
                    <div style={{ minWidth: '160px' }}>
                        <label style={{
                            display: 'block',
                            fontSize: '11px',
                            color: 'var(--cds-global-typography-color-300, #adbbc4)',
                            marginBottom: '4px',
                        }}>IPs (comma-separated)</label>
                        <input
                            type="text"
                            value={ipsText}
                            onChange={e => setIpsText(e.target.value)}
                            placeholder="10.0.0.1, 10.0.0.0/24"
                            style={{
                                width: '100%',
                                padding: '6px 10px',
                                background: 'var(--cds-alias-object-interaction-background, #1b2a32)',
                                border: '1px solid var(--cds-alias-object-border-color, #565656)',
                                borderRadius: '3px',
                                color: 'var(--cds-global-typography-color-400, #e0e8ec)',
                                fontSize: '13px',
                                boxSizing: 'border-box',
                            }}
                        />
                    </div>
                    <div style={{ minWidth: '180px' }}>
                        <label style={{
                            display: 'block',
                            fontSize: '11px',
                            color: 'var(--cds-global-typography-color-300, #adbbc4)',
                            marginBottom: '4px',
                        }}>Pod Label Selector</label>
                        <input
                            type="text"
                            value={podLabelSelector}
                            onChange={e => setPodLabelSelector(e.target.value)}
                            placeholder="app=frontend,version!=v2"
                            style={{
                                width: '100%',
                                padding: '6px 10px',
                                background: 'var(--cds-alias-object-interaction-background, #1b2a32)',
                                border: '1px solid var(--cds-alias-object-border-color, #565656)',
                                borderRadius: '3px',
                                color: 'var(--cds-global-typography-color-400, #e0e8ec)',
                                fontSize: '13px',
                                boxSizing: 'border-box',
                            }}
                        />
                    </div>
                </div>
                <div cds-layout="horizontal gap:sm" style={{ alignSelf: 'flex-end' }}>
                    <CdsButton type="button" action="solid" size="sm" onClick={applyFilters}>
                        Apply Filters
                    </CdsButton>
                    <CdsButton type="button" action="outline" size="sm" onClick={onPauseToggle}>
                        {paused ? 'Resume' : 'Pause'}
                    </CdsButton>
                    <CdsButton type="button" action="outline" size="sm" onClick={onClear}>
                        Clear
                    </CdsButton>
                </div>
            </div>
            <div cds-layout="horizontal gap:lg align:vertical-center" cds-text="secondary">
                <span style={{ display: 'inline-flex', alignItems: 'center', gap: '6px' }}>
                    <span style={{
                        width: '8px',
                        height: '8px',
                        borderRadius: '50%',
                        backgroundColor: statusColor,
                        display: 'inline-block',
                    }} />
                    {statusText}
                </span>
                <span>{connectionCount} connections</span>
                {droppedCount > 0 && (
                    <span style={{ color: '#e6a700' }}>
                        {droppedCount} flows dropped (buffer overflow)
                    </span>
                )}
                {evictionWarning && (
                    <span style={{ color: '#e6a700' }}>
                        Store limit reached, oldest entries evicted
                    </span>
                )}
            </div>
        </div>
    );
}
