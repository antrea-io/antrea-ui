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
import { CdsButton } from '@cds/react/button';
import { CdsDivider } from '@cds/react/divider';
import { NodeLink, NodeLatencyModel, nodeLinksFor } from '../routes/nodelatency-util';

function fmtRtt(link: NodeLink): string {
    return link.down || link.rttMs === undefined ? 'N/A' : link.rttMs.toFixed(3);
}

function latestRecv(link: NodeLink): string {
    const times = link.targets.map(t => t.lastRecvTime).filter((t): t is string => !!t).sort();
    const t = times.length ? times[times.length - 1] : undefined;
    return t ? new Date(t).toLocaleString() : 'None';
}

function targetIPs(link: NodeLink): string {
    return link.targets.map(t => t.targetIP).join(', ') || 'None';
}

function LinkTable(props: { title: string, peerHeader: string, links: NodeLink[], peerOf: (l: NodeLink) => string }) {
    return (
        <div cds-layout="vertical gap:sm">
            <div cds-text="section">{props.title}</div>
            {props.links.length === 0 ? (
                <p cds-text="secondary">No {props.title.toLowerCase()}.</p>
            ) : (
                <table cds-table="border:all" cds-text="center body">
                    <thead>
                        <tr>
                            <th>{props.peerHeader}</th>
                            <th>Status</th>
                            <th>Latency (ms)</th>
                            <th>Target IP</th>
                            <th>Last Recv</th>
                        </tr>
                    </thead>
                    <tbody>
                        {props.links.map((link, idx) => (
                            <tr key={idx}>
                                <td>{props.peerOf(link)}</td>
                                <td style={link.down ? { color: '#e12200' } : undefined}>{link.down ? 'Down' : 'Up'}</td>
                                <td>{fmtRtt(link)}</td>
                                <td>{targetIPs(link)}</td>
                                <td>{latestRecv(link)}</td>
                            </tr>
                        ))}
                    </tbody>
                </table>
            )}
        </div>
    );
}

export function NodeDetailPanel(props: { model: NodeLatencyModel, node: string, onClose: () => void }) {
    const { egress, ingress } = nodeLinksFor(props.model.links, props.node);
    const health = props.model.health.get(props.node);

    return (
        <CdsCard>
            <div cds-layout="vertical gap:md">
                <div cds-layout="horizontal align:vertical-center gap:md">
                    <div cds-text="section" cds-layout="align:left">Node: {props.node}</div>
                    <CdsButton type="button" action="outline" size="sm" onClick={props.onClose}>Clear</CdsButton>
                </div>
                {health && (
                    <p cds-text="secondary">
                        Egress: {health.egressTotal - health.egressDown}/{health.egressTotal} up
                        {' '}&middot;{' '}
                        Ingress: {health.ingressTotal - health.ingressDown}/{health.ingressTotal} up
                    </p>
                )}
                <CdsDivider cds-card-remove-margin></CdsDivider>
                <LinkTable
                    title="Egress Links"
                    peerHeader="Target Node"
                    links={egress}
                    peerOf={l => l.targetNode}
                />
                <LinkTable
                    title="Ingress Links"
                    peerHeader="Source Node"
                    links={ingress}
                    peerOf={l => l.sourceNode}
                />
            </div>
        </CdsCard>
    );
}

export function ProblemNodesPanel(props: { model: NodeLatencyModel, onSelect: (node: string) => void }) {
    const problems = props.model.problemNodes;
    if (problems.length === 0) return null;

    return (
        <CdsCard>
            <div cds-layout="vertical gap:md">
                <div cds-text="section" style={{ color: '#e12200' }}>
                    Problem Nodes ({problems.length})
                </div>
                <CdsDivider cds-card-remove-margin></CdsDivider>
                <table cds-table="border:all" cds-text="center body">
                    <thead>
                        <tr>
                            <th>Node</th>
                            <th>Egress Down</th>
                            <th>Ingress Down</th>
                            <th></th>
                        </tr>
                    </thead>
                    <tbody>
                        {problems.map(h => (
                            <tr key={h.node}>
                                <td>{h.node}</td>
                                <td>{h.egressDown}/{h.egressTotal}</td>
                                <td>{h.ingressDown}/{h.ingressTotal}</td>
                                <td>
                                    <CdsButton type="button" action="flat" size="sm" onClick={() => props.onSelect(h.node)}>
                                        Inspect
                                    </CdsButton>
                                </td>
                            </tr>
                        ))}
                    </tbody>
                </table>
            </div>
        </CdsCard>
    );
}
