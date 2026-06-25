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

import { CdsCard } from '@cds/react/card';
import { CdsDivider } from '@cds/react/divider';
import { AggregatedStats } from '../routes/nodelatency-util';

function fmt(v: number): string {
    return isNaN(v) ? 'N/A' : v.toFixed(2);
}

export default function NodeLatencyStatsSummary(props: { agg: AggregatedStats }) {
    const a = props.agg;
    const items: { label: string, value: string, alert?: boolean }[] = [
        { label: 'Nodes', value: a.nodeCount.toString() },
        { label: 'Measured Links', value: a.measuredCount.toString() },
        { label: 'Down Links', value: a.downCount.toString(), alert: a.downCount > 0 },
        { label: 'Mean (ms)', value: fmt(a.meanMs) },
        { label: 'Median (ms)', value: fmt(a.medianMs) },
        { label: 'P90 (ms)', value: fmt(a.p90Ms) },
        { label: 'Max (ms)', value: fmt(a.maxMs) },
    ];
    return (
        <CdsCard>
            <div cds-layout="vertical gap:md">
                <div cds-text="section" cds-layout="p-y:sm">Summary</div>
                <CdsDivider cds-card-remove-margin></CdsDivider>
                <div cds-layout="horizontal gap:xl wrap:wrap p-y:sm">
                    {items.map(item => (
                        <div key={item.label} cds-layout="vertical gap:xs">
                            <div cds-text="caption" style={{ opacity: 0.7 }}>{item.label}</div>
                            <div cds-text="heading" style={item.alert ? { color: '#e12200' } : undefined}>
                                {item.value}
                            </div>
                        </div>
                    ))}
                </div>
            </div>
        </CdsCard>
    );
}
