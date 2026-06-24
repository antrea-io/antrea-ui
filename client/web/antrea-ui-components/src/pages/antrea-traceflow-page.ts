// Copyright 2026 Antrea Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import { LitElement, html, css, nothing } from 'lit';
import { property, state, query } from 'lit/decorators.js';
import { isIP, ipVersion } from 'is-ip';
// @ts-expect-error no bundled types shipped with d3-graphviz
import { graphviz } from 'd3-graphviz';
import { pageStyles } from '../lib/styles';
import { apiFetch, APIError } from '../lib/api';
import '../antrea-button';
import '../antrea-alert';

// ── Types ─────────────────────────────────────────────────────────────────────

interface TraceflowPacket {
    srcIP?: string; dstIP?: string;
    ipHeader?: { protocol?: number };
    ipv6Header?: { nextHeader?: number };
    transportHeader: {
        icmp?: {};
        udp?: { srcPort?: number; dstPort?: number };
        tcp?: { srcPort?: number; dstPort?: number; flags?: number };
    };
}

interface TraceflowSpec {
    source: { namespace?: string; pod?: string; ip?: string };
    destination: { namespace?: string; pod?: string; service?: string; ip?: string };
    packet?: TraceflowPacket;
    liveTraffic?: boolean;
    droppedOnly?: boolean;
    timeout?: number;
}

interface TraceflowObservation {
    component: string; componentInfo: string; action: string; pod: string;
    dstMAC: string; networkPolicy: string; egress: string; ttl: number;
    translatedSrcIP: string; translatedDstIP: string; tunnelDstIP: string;
    egressIP: string; egressNode: string; srcPodIP: string;
}

interface TraceflowNodeResult { node: string; role: string; timestamp: number; observations: TraceflowObservation[]; }
interface TraceflowStatus { phase: string; reason: string; startTime: string; results: TraceflowNodeResult[]; capturedPacket?: TraceflowPacket; }
interface TraceflowResult { apiVersion?: string; kind?: string; metadata?: { name?: string }; spec?: TraceflowSpec; status?: TraceflowStatus; }

// ── DOT graph builder (ported from traceflowresult.tsx) ───────────────────────

const ghostWhite = '"#F8F8FF"';
const lightGrey = '"#C8C8C8"';
const grey = '"#808080"';

class TFNode {
    name: string;
    private attrs = new Map<string, string>();
    constructor(name: string) { this.name = name; }
    setAttr(k: string, v: string) { this.attrs.set(k, v); }
    asDot() { return `${this.name} [${Array.from(this.attrs.entries()).map(([k, v]) => `${k}=${v}`).join(',')}]`; }
}

class TFEdge {
    constructor(private s: string, private t: string) {}
    asDot() { return `${this.s} -> ${this.t}`; }
}

class TFGraph {
    private nodes: TFNode[] = [];
    private edges: TFEdge[] = [];
    private subgraphs: TFSubgraph[] = [];
    private attrs = new Map<string, string>();
    constructor(private type: string, private name: string) {}
    addNode(n: TFNode) { this.nodes.push(n); }
    addEdge(e: TFEdge) { this.edges.push(e); }
    addSubgraph(g: TFSubgraph) { this.subgraphs.push(g); }
    setAttr(k: string, v: string) { this.attrs.set(k, v); }
    asDot(indent = ''): string {
        const i = indent + '\t';
        const lines = [`${indent}${this.type} ${this.name} {`];
        this.attrs.forEach((v, k) => lines.push(`${i}${k}=${v}`));
        this.subgraphs.forEach(g => { lines.push(g.asDot(i)); lines.push(''); });
        this.nodes.forEach(n => lines.push(`${i}${n.asDot()}`));
        this.edges.forEach(e => lines.push(`${i}${e.asDot()}`));
        lines.push(`${indent}}`);
        return lines.join('\n');
    }
}
class TFSubgraph extends TFGraph { constructor(name: string) { super('subgraph', name); } }
class TFDigraph extends TFGraph { constructor(name: string) { super('digraph', name); } }

function isSender(r: TraceflowNodeResult) { return r.observations[0]?.component === 'SpoofGuard' && r.observations[0]?.action === 'Forwarded'; }
function isReceiver(r: TraceflowNodeResult) { return r.observations[0]?.component === 'Forwarding' && r.observations[0]?.action === 'Received'; }

function obsLabel(obs: TraceflowObservation): string {
    const parts = [obs.component];
    if (obs.componentInfo) parts.push(obs.componentInfo);
    parts.push(obs.action);
    if (obs.component === 'NetworkPolicy' && obs.networkPolicy) parts.push(`Netpol: ${obs.networkPolicy}`);
    if (obs.pod) parts.push(`To: ${obs.pod}`);
    if (obs.action !== 'Dropped') {
        if (obs.translatedSrcIP) parts.push(`Translated Source IP: ${obs.translatedSrcIP}`);
        if (obs.translatedDstIP) parts.push(`Translated Destination IP: ${obs.translatedDstIP}`);
        if (obs.tunnelDstIP) parts.push(`Tunnel Destination IP: ${obs.tunnelDstIP}`);
        if (obs.egressIP) parts.push(`Egress IP: ${obs.egressIP}`);
        if (obs.egress) parts.push(`Egress: ${obs.egress}`);
        if (obs.egressNode) parts.push(`Egress Node: ${obs.egressNode}`);
    }
    return parts.join('\n');
}

function endpointNode(name: string, label: string): TFNode {
    const n = new TFNode(name);
    n.setAttr('style', '"filled,bold"'); n.setAttr('label', `"${label}"`);
    n.setAttr('color', grey); n.setAttr('fillcolor', lightGrey);
    return n;
}

function buildSubgraph(name: string, ep: TFNode, nr: TraceflowNodeResult, isDst: boolean): [TFSubgraph, TFNode] {
    const g = new TFSubgraph(name);
    g.setAttr('style', '"filled,bold"'); g.setAttr('bgcolor', ghostWhite); g.setAttr('label', `"${nr.node}"`);
    const nodes: TFNode[] = [];
    if (!isDst) nodes.push(ep);
    nr.observations.forEach((obs, i) => {
        const n = new TFNode(`${name}_${i}`);
        n.setAttr('shape', '"box"'); n.setAttr('style', '"rounded,filled,solid"');
        n.setAttr('label', `"${obsLabel(obs)}"`); n.setAttr('color', grey); n.setAttr('fillcolor', lightGrey);
        nodes.push(n);
    });
    if (isDst) nodes.push(ep);
    nodes.forEach(n => g.addNode(n));
    for (let i = 0; i < nodes.length - 1; i++) g.addEdge(new TFEdge(nodes[i].name, nodes[i + 1].name));
    return [g, isDst ? nodes[0] : nodes[nodes.length - 1]];
}

function buildDot(spec: TraceflowSpec, status: TraceflowStatus): string {
    const graph = new TFDigraph('tf');
    if (!status.results) return graph.asDot();
    const sender = status.results.find(isSender);
    const receiver = status.results.find(isReceiver);

    const srcLabel = spec.source.ip ?? (spec.source.pod ? `${spec.source.namespace}/${spec.source.pod}` : status.capturedPacket?.srcIP ?? '');
    const dstLabel = spec.destination.ip ?? (spec.destination.service ? `${spec.destination.namespace}/${spec.destination.service}` : (() => {
        let pod = '';
        if (spec.destination.pod) pod = `${spec.destination.namespace}/${spec.destination.pod}`;
        if (!pod) status.results.forEach(nr => nr.observations.forEach(o => { if (o.pod) pod = o.pod; }));
        return pod || (status.capturedPacket?.dstIP ?? '');
    })());

    if (!sender) {
        if (!receiver) return graph.asDot();
        const dstEp = endpointNode('dest', dstLabel);
        const [dstCluster, dstFirst] = buildSubgraph('cluster_destination', dstEp, receiver, true);
        graph.addSubgraph(dstCluster);
        const srcEp = endpointNode('source', srcLabel);
        graph.addNode(srcEp);
        graph.addEdge(new TFEdge(srcEp.name, dstFirst.name));
        return graph.asDot();
    }

    const srcEp = endpointNode('source', srcLabel);
    const [srcCluster, srcLast] = buildSubgraph('cluster_source', srcEp, sender, false);
    graph.addSubgraph(srcCluster);
    const dstEp = endpointNode('dest', dstLabel);
    if (!receiver) {
        srcCluster.addNode(dstEp);
        srcCluster.addEdge(new TFEdge(srcLast.name, dstEp.name));
        return graph.asDot();
    }
    const [dstCluster, dstFirst] = buildSubgraph('cluster_destination', dstEp, receiver, true);
    graph.addSubgraph(dstCluster);
    graph.addEdge(new TFEdge(srcLast.name, dstFirst.name));
    return graph.asDot();
}

// ── Component ─────────────────────────────────────────────────────────────────

type Proto = 'TCP' | 'UDP' | 'ICMP';
type DstType = 'Pod' | 'Service' | 'IP';

export class AntreaTraceflowPage extends LitElement {
    static styles = [pageStyles, css`
        .tf-layout { display: flex; gap: 1.5rem; flex-wrap: wrap; }
        .tf-form { flex: 0 0 auto; min-width: 340px; }
        .tf-result { flex: 1 1 0; min-width: 0; }
    `];

    @property() token = '';

    // Form state
    @state() private _srcNs = 'default';
    @state() private _src = '';
    @state() private _proto: Proto = 'TCP';
    @state() private _srcPort = 0;
    @state() private _dstType: DstType = 'Pod';
    @state() private _dstNs = 'default';
    @state() private _dst = '';
    @state() private _dstPort = 80;
    @state() private _tcpFlags = 2;
    @state() private _timeout = 20;
    @state() private _ipv6 = false;
    @state() private _live = false;
    @state() private _droppedOnly = false;

    // Run state
    @state() private _running = false;
    @state() private _resultSpec?: TraceflowSpec;
    @state() private _resultStatus?: TraceflowStatus;
    @state() private _formError = '';

    @query('#graph-container') private _graphContainer?: HTMLDivElement;

    override updated(changed: Map<string, unknown>) {
        if ((changed.has('_resultStatus') || changed.has('_resultSpec')) && this._resultStatus?.phase === 'Succeeded') {
            this._renderGraph();
        }
    }

    private _renderGraph() {
        if (!this._graphContainer || !this._resultSpec || !this._resultStatus) return;
        try {
            const dot = buildDot(this._resultSpec, this._resultStatus);
            graphviz(this._graphContainer).renderDot(dot);
        } catch (e) {
            console.error('Failed to render traceflow graph', e);
        }
    }

    private _defaultDstPort(): number {
        if (this._live) return 0;
        if (this._proto === 'TCP') return 80;
        if (this._proto === 'UDP') return 53;
        return 0;
    }

    private _buildSpec(): TraceflowSpec {
        const { _src, _dst, _proto, _srcPort, _dstPort, _tcpFlags, _timeout, _ipv6, _live, _droppedOnly, _srcNs, _dstNs, _dstType } = this;

        if (_droppedOnly && !_live) throw new Error('Dropped-only requires Live Traffic mode');
        if (!_src && !_live) throw new Error('Source is required');
        if (!_dst && !_live) throw new Error('Destination is required');
        if (!_src && !_dst) throw new Error('At least one of source and destination is required');

        const srcIsIP = isIP(_src);
        if (srcIsIP && !_live) throw new Error('Source must be a Pod for a regular Traceflow');
        if (srcIsIP && _dstType !== 'Pod') throw new Error('At least one of source and destination must be a Pod');

        const srcV = ipVersion(_src);
        const dstV = ipVersion(_dst);
        if (srcV && dstV && srcV !== dstV) throw new Error('IP version mismatch between source and destination');
        if (srcV === 4 && _ipv6) throw new Error("Don't check IPv6 when providing an IPv4 source");
        if (dstV === 4 && _ipv6) throw new Error("Don't check IPv6 when providing an IPv4 destination");
        const useIPv6 = dstV === 6 || srcV === 6 || _ipv6;

        let protocol = 0;
        const transportHeader: TraceflowPacket['transportHeader'] = {};
        if (_proto === 'ICMP') {
            protocol = useIPv6 ? 58 : 1;
            transportHeader.icmp = {};
        } else if (_proto === 'TCP') {
            protocol = 6;
            transportHeader.tcp = { flags: _tcpFlags };
            if (_srcPort > 0) transportHeader.tcp.srcPort = _srcPort;
            if (_dstPort > 0) transportHeader.tcp.dstPort = _dstPort;
        } else {
            protocol = 17;
            transportHeader.udp = {};
            if (_srcPort > 0) transportHeader.udp.srcPort = _srcPort;
            if (_dstPort > 0) transportHeader.udp.dstPort = _dstPort;
        }

        const packet: TraceflowPacket = { transportHeader };
        if (useIPv6) packet.ipv6Header = { nextHeader: protocol };
        else packet.ipHeader = { protocol };

        const spec: TraceflowSpec = { source: {}, destination: {} };
        if (_src) {
            if (srcIsIP) spec.source.ip = _src;
            else { spec.source.namespace = _srcNs; spec.source.pod = _src; }
        }
        if (_dstType && _dst) {
            if (_dstType === 'Pod') { spec.destination.namespace = _dstNs; spec.destination.pod = _dst; }
            else if (_dstType === 'Service') { spec.destination.namespace = _dstNs; spec.destination.service = _dst; }
            else {
                if (!isIP(_dst)) throw new Error('Invalid destination IP address');
                spec.destination.ip = _dst;
            }
        }
        spec.packet = packet;
        if (_live) spec.liveTraffic = true;
        if (_droppedOnly) spec.droppedOnly = true;
        spec.timeout = _timeout;
        return spec;
    }

    private async _run(spec: TraceflowSpec) {
        try {
            const createResp = await apiFetch('traceflow', this.token, {
                method: 'POST',
                headers: { 'content-type': 'application/json' },
                body: JSON.stringify({ spec }),
            });
            if (createResp.status !== 202) throw new Error('Expected 202 from traceflow create');
            const location = createResp.headers.get('location');
            if (!location) throw new Error('Missing Location header');
            const statusURL = `${location}/status`;

            let pollResp = createResp;
            for (;;) {
                const retryAfter = pollResp.headers.get('retry-after') ?? '0';
                let wait = parseInt(retryAfter) * 1000;
                if (isNaN(wait) || wait === 0) wait = 100;
                await new Promise(r => setTimeout(r, wait));
                pollResp = await apiFetch(statusURL.replace('/api/v1/', ''), this.token);
                const done = pollResp.url.endsWith('/result');
                if (done) {
                    // Clean up
                    apiFetch(location.replace('/api/v1/', ''), this.token, { method: 'DELETE' })
                        .catch(() => { /* best-effort */ });
                    const tf = await pollResp.json() as TraceflowResult;
                    return tf.status;
                }
            }
        } catch (e) {
            if (e instanceof APIError && e.code === 401) {
                this.dispatchEvent(new CustomEvent('antrea-session-expired', { bubbles: true, composed: true }));
                return undefined;
            }
            throw e;
        }
    }

    private async _submit(e: Event) {
        e.preventDefault();
        this._formError = '';
        this._resultStatus = undefined;
        let spec: TraceflowSpec;
        try {
            spec = this._buildSpec();
        } catch (err) {
            this._formError = err instanceof Error ? err.message : String(err);
            return;
        }
        this._running = true;
        try {
            const status = await this._run(spec);
            if (status) { this._resultSpec = spec; this._resultStatus = status; }
        } catch (err) {
            this._formError = err instanceof Error ? err.message : String(err);
            this.dispatchEvent(new CustomEvent('antrea-error', { detail: { message: this._formError }, bubbles: true, composed: true }));
        } finally {
            this._running = false;
        }
    }

    private _reset() {
        this._srcNs = 'default'; this._src = ''; this._proto = 'TCP';
        this._srcPort = 0; this._dstType = 'Pod'; this._dstNs = 'default';
        this._dst = ''; this._dstPort = 80; this._tcpFlags = 2;
        this._timeout = 20; this._ipv6 = false; this._live = false;
        this._droppedOnly = false; this._formError = '';
        this._resultSpec = undefined; this._resultStatus = undefined;
    }

    private _onProtoChange(e: Event) {
        this._proto = (e.target as HTMLSelectElement).value as Proto;
        this._dstPort = this._defaultDstPort();
        this._tcpFlags = this._proto === 'TCP' && !this._live ? 2 : 0;
    }

    private _onLiveChange(e: Event) {
        this._live = (e.target as HTMLInputElement).checked;
        this._dstPort = this._defaultDstPort();
        this._tcpFlags = this._proto === 'TCP' && !this._live ? 2 : 0;
    }

    private _renderResult() {
        if (!this._resultStatus) return nothing;
        const { phase, reason } = this._resultStatus;
        if (phase === 'Failed') {
            return html`
                <div class="page-layout">
                    <p class="page-title">Result</p>
                    <antrea-alert status="danger">Traceflow Failed</antrea-alert>
                    <antrea-alert status="danger">${reason}</antrea-alert>
                </div>`;
        }
        if (phase === 'Succeeded') {
            return html`
                <div class="page-layout">
                    <p class="page-title">Result</p>
                    <div id="graph-container"></div>
                </div>`;
        }
        return html`<p>Unknown phase: ${phase}</p>`;
    }

    override render() {
        const showPorts = this._proto === 'TCP' || this._proto === 'UDP';
        const showTcpFlags = this._proto === 'TCP' && !this._live;
        const dstPortRequired = !this._live && showPorts;

        return html`
            <main>
                <div class="tf-layout">
                    <div class="tf-form page-layout">
                        <p class="page-title">Traceflow</p>
                        ${this._formError ? html`<antrea-alert status="danger">${this._formError}</antrea-alert>` : nothing}
                        ${this._running ? html`<antrea-alert status="loading">Running Traceflow, this may take a few seconds…</antrea-alert>` : nothing}

                        <form class="form-stack" @submit=${this._submit}>
                            <div class="field-group">
                                <label class="field-label" for="src-ns">Source Namespace</label>
                                <input id="src-ns" class="field-input" .value=${this._srcNs} @input=${(e: Event) => { this._srcNs = (e.target as HTMLInputElement).value; }} />
                            </div>
                            <div class="field-group">
                                <label class="field-label" for="src">Source</label>
                                <input id="src" class="field-input" .value=${this._src}
                                    placeholder=${this._live ? 'Pod Name, or IP' : 'Pod Name'}
                                    @input=${(e: Event) => { this._src = (e.target as HTMLInputElement).value; }} />
                            </div>

                            <div class="field-group">
                                <label class="field-label" for="protocol">Protocol</label>
                                <select id="protocol" class="field-select" .value=${this._proto} @change=${this._onProtoChange}>
                                    <option value="TCP">TCP</option>
                                    <option value="UDP">UDP</option>
                                    <option value="ICMP">ICMP</option>
                                </select>
                            </div>

                            ${showPorts ? html`
                                <div class="field-group">
                                    <label class="field-label" for="src-port">Source Port</label>
                                    <input id="src-port" class="field-input" type="number" min="0" max="65535"
                                        .value=${this._srcPort.toString()}
                                        @input=${(e: Event) => { this._srcPort = parseInt((e.target as HTMLInputElement).value) || 0; }} />
                                    <span class="field-hint">${this._live ? 'use 0 to match any port' : 'use 0 for arbitrary port'}</span>
                                </div>
                            ` : nothing}

                            <div class="field-group">
                                <span class="field-label">Destination Type</span>
                                <div class="radio-group">
                                    ${(['Pod', 'Service', 'IP'] as DstType[]).map(t => html`
                                        <label class="radio-label">
                                            <input type="radio" name="dst-type" value=${t}
                                                .checked=${this._dstType === t}
                                                @change=${() => { this._dstType = t; }} />
                                            ${t}
                                        </label>
                                    `)}
                                </div>
                            </div>

                            <div class="field-group">
                                <label class="field-label" for="dst-ns">Destination Namespace</label>
                                <input id="dst-ns" class="field-input" .value=${this._dstNs}
                                    @input=${(e: Event) => { this._dstNs = (e.target as HTMLInputElement).value; }} />
                            </div>
                            <div class="field-group">
                                <label class="field-label" for="dst">Destination</label>
                                <input id="dst" class="field-input" .value=${this._dst}
                                    placeholder="Pod / Service Name, or IP"
                                    @input=${(e: Event) => { this._dst = (e.target as HTMLInputElement).value; }} />
                            </div>

                            ${showPorts ? html`
                                <div class="field-group">
                                    <label class="field-label" for="dst-port">
                                        Destination Port${dstPortRequired ? ' *' : ''}
                                    </label>
                                    <input id="dst-port" class="field-input" type="number"
                                        min=${dstPortRequired ? 1 : 0} max="65535"
                                        .value=${this._dstPort.toString()}
                                        @input=${(e: Event) => { this._dstPort = parseInt((e.target as HTMLInputElement).value) || 0; }} />
                                    ${this._live ? html`<span class="field-hint">use 0 to match any port</span>` : nothing}
                                </div>
                            ` : nothing}

                            ${showTcpFlags ? html`
                                <div class="field-group">
                                    <label class="field-label" for="tcp-flags">TCP Flags</label>
                                    <input id="tcp-flags" class="field-input" type="number" min="0" max="255"
                                        .value=${this._tcpFlags.toString()}
                                        @input=${(e: Event) => { this._tcpFlags = parseInt((e.target as HTMLInputElement).value) || 0; }} />
                                    <span class="field-hint">use 2 for SYN flag</span>
                                </div>
                            ` : nothing}

                            <div class="field-group">
                                <label class="field-label" for="timeout">Request Timeout (s)</label>
                                <input id="timeout" class="field-input" type="number" min="1" max="120"
                                    .value=${this._timeout.toString()}
                                    @input=${(e: Event) => { this._timeout = parseInt((e.target as HTMLInputElement).value) || 20; }} />
                            </div>

                            <div style="display:flex;gap:1.5rem;flex-wrap:wrap">
                                <label class="checkbox-label">
                                    <input type="checkbox" .checked=${this._ipv6}
                                        @change=${(e: Event) => { this._ipv6 = (e.target as HTMLInputElement).checked; }} />
                                    Use IPv6
                                </label>
                                <label class="checkbox-label">
                                    <input type="checkbox" .checked=${this._live} @change=${this._onLiveChange} />
                                    Live Traffic
                                </label>
                                ${this._live ? html`
                                    <label class="checkbox-label">
                                        <input type="checkbox" .checked=${this._droppedOnly}
                                            @change=${(e: Event) => { this._droppedOnly = (e.target as HTMLInputElement).checked; }} />
                                        Dropped Traffic Only
                                    </label>
                                ` : nothing}
                            </div>

                            <div class="btn-group">
                                <antrea-button type="submit" ?disabled=${this._running}>Run Traceflow</antrea-button>
                                <antrea-button type="button" action="outline" @click=${this._reset}>Reset</antrea-button>
                            </div>
                        </form>
                    </div>

                    <div class="tf-result">
                        ${this._renderResult()}
                    </div>
                </div>
            </main>
        `;
    }
}

customElements.define('antrea-traceflow-page', AntreaTraceflowPage);

declare global {
    interface HTMLElementTagNameMap { 'antrea-traceflow-page': AntreaTraceflowPage; }
}
