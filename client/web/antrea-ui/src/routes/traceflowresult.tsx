import React, { useEffect, useRef} from 'react';
import { useLocation } from "react-router-dom";
import { TraceflowSpec, TraceflowStatus, TraceflowNodeResult, TraceflowObservation } from '../api/traceflow';
// eslint-disable-next-line
import * as d3 from 'd3';
import { graphviz } from "d3-graphviz";
import { CdsAlertGroup, CdsAlert } from "@cds/react/alert";
import { useAPIError} from '../components/errors';

class Node {
    name: string;
    private attrs: Map<string, string>;

    constructor(name: string) {
        this.name = name;
        this.attrs = new Map<string, string>();
    }

    setAttr(name: string, value:string) {
        this.attrs.set(name, value);
    }

    asDot(): string {
        const attrs = new Array<string>();
        this.attrs.forEach((v, k) => attrs.push(`${k}=${v}`));
        return `${this.name} [${attrs.join(',')}]`;
    }
}

class Edge {
    private startNode: string;
    private endNode: string;
    private attrs: Map<string, string>;

    constructor(startNode: string, endNode: string) {
        this.startNode = startNode;
        this.endNode = endNode;
        this.attrs = new Map<string, string>();
    }

    asDot(): string {
        const attrs = new Array<string>();
        this.attrs.forEach((v, k) => attrs.push(`${k}=${v}`));
        return `${this.startNode} -> ${this.endNode} [${attrs.join(',')}]`;
    }
}

class DotStringBuilder {
    private lines: string[];
    private indent: number;

    constructor() {
        this.lines = new Array<string>();
        this.indent = 0;
    }

    addIndent() {
        this.indent += 1;
    }

    removeIndent() {
        this.indent -= 1;
    }

    pushLine(line: string) {
        const indent = '\t'.repeat(this.indent);
        this.lines.push(indent + line);
    }

    emit(): string {
        return this.lines.join('\n');
    }
}

class Graph {
    private graphType: string;
    private name: string;
    private nodes: Node[];
    private edges: Edge[];
    private subgraphs: Graph[];
    private attrs: Map<string, string>;

    constructor(graphType: string, name: string) {
        this.graphType = graphType;
        this.name = name;
        this.nodes = new Array<Node>();
        this.edges = new Array<Edge>();
        this.subgraphs = new Array<Graph>();
        this.attrs = new Map<string, string>();
    }

    addNode(node: Node) {
        this.nodes.push(node);
    }

    addEdge(edge: Edge) {
        this.edges.push(edge);
    }

    addSubgraph(graph: Subgraph) {
        this.subgraphs.push(graph);
    }

    setAttr(name: string, value:string) {
        this.attrs.set(name, value);
    }

    asDotBuilder(builder: DotStringBuilder) {
        builder.pushLine(`${this.graphType} ${this.name} {`);
        builder.addIndent();
        this.attrs.forEach((v, k) => builder.pushLine(`${k}=${v}`));
        this.subgraphs.forEach(g => {
            g.asDotBuilder(builder);
            builder.pushLine('');
        });
        this.nodes.forEach(n => builder.pushLine(n.asDot()));
        this.edges.forEach(e => builder.pushLine(e.asDot()));
        builder.removeIndent();
        builder.pushLine('}');
    }

    asDot(): string {
        const builder = new DotStringBuilder();
        this.asDotBuilder(builder);
        return builder.emit();
    }
}

class Digraph extends Graph {
    constructor(name: string) {
        super('digraph', name);
    }
}

class Subgraph extends Graph {
    constructor(name: string) {
        super('subgraph', name);
    }
}

function isSender(nodeResult: TraceflowNodeResult): boolean {
    if (nodeResult.observations.length === 0) {
        return false;
    }
    const firstObservation = nodeResult.observations[0];
    if (firstObservation.component !== "SpoofGuard" || firstObservation.action !== "Forwarded") {
        return false;
    }
    return true;
}

function isReceiver(nodeResult: TraceflowNodeResult): boolean {
    if (nodeResult.observations.length === 0) {
        return false;
    }
    const firstObservation = nodeResult.observations[0];
    if (firstObservation.component !== "Forwarding" || firstObservation.action !== "Received") {
        return false;
    }
    return true;
}

// const darkRed = `"#B20000"`
// const mistyRose = `"#EDD5D5"`
// const fireBrick = `"#B22222"`
const ghostWhite = `"#F8F8FF"`;
// const gainsboro = `"#DCDCDC"`
const lightGrey = `"#C8C8C8"`;
// const silver = `"#C0C0C0"`
const grey = `"#808080"`;
// const dimGrey = `"#696969"`

class TraceflowResultError extends Error {
    constructor(...params: any[]) {
        super(...params);
        this.message = `invalid Traceflow result: ${this.message}`;
    }
}

class TraceflowGraphBuilder {
    private spec: TraceflowSpec;
    private status: TraceflowStatus;

    constructor(spec: TraceflowSpec, status: TraceflowStatus) {
        this.spec = spec;
        this.status = status;
    }

    buildGraph(): Digraph {
        const graph = new Digraph('tf');

        if (!this.status) return graph;
        if (!this.status.results) return graph;

        const senderNodeResult = this.status.results.find(isSender);
        const receiverNodeResult = this.status.results.find(isReceiver);

        if (!senderNodeResult && !this.spec.liveTraffic) {
            throw new TraceflowResultError("missing sender information for regular Traceflow");
        }

        if (!senderNodeResult) {
            // Live Traceflow case
            if (!receiverNodeResult) {
                throw new TraceflowResultError("there is no Node result for live Traceflow");
            }
            const dstNode = this.buildEndpointNode('dest', this.getDestinationLabel());
            const [dstCluster, dstFirstNode] = this.buildSubgraph('cluster_destination', dstNode, receiverNodeResult, true);
            graph.addSubgraph(dstCluster);
            const srcNode = this.buildEndpointNode('source', this.getSourceLabel());
            graph.addNode(srcNode);
            graph.addEdge(new Edge(srcNode.name, dstFirstNode.name));
            return graph;
        }

        const srcNode = this.buildEndpointNode('source', this.getSourceLabel());
        const [srcCluster, srcLastNode] = this.buildSubgraph('cluster_source', srcNode, senderNodeResult, false);
        graph.addSubgraph(srcCluster);

        const dstNode = this.buildEndpointNode('dest', this.getDestinationLabel());

        if (!receiverNodeResult) {
            srcCluster.addNode(dstNode);
            srcCluster.addEdge(new Edge(srcLastNode.name, dstNode.name));
            return graph;
        }

        // sender + receiver
        const [dstCluster, dstFirstNode] = this.buildSubgraph('cluster_destination', dstNode, receiverNodeResult, true);
        graph.addSubgraph(dstCluster);
        graph.addEdge(new Edge(srcLastNode.name, dstFirstNode.name));

        return graph;
    }

    private getTraceflowLabel(obs: TraceflowObservation): string {
        const label: string[] = [obs.component];
        if (obs.componentInfo) label.push(obs.componentInfo);
        label.push(obs.action);
        if (obs.component === 'NetworkPolicy' && obs.networkPolicy) label.push(`Netpol: ${obs.networkPolicy}`);
        if (obs.pod) label.push(`To: ${obs.pod}`);
        if (obs.action !== 'Dropped') {
            if (obs.translatedSrcIP) label.push(`Translated Source IP: ${obs.translatedSrcIP}`);
            if (obs.translatedDstIP) label.push(`Translated Destination IP: ${obs.translatedDstIP}`);
            if (obs.tunnelDstIP) label.push(`Tunnel Destination IP: ${obs.tunnelDstIP}`);
            if (obs.egressIP) label.push(`Egress IP: ${obs.egressIP}`);
            if (obs.egress) label.push(`Egress: ${obs.egress}`);
        }
        return label.join('\n');
    }

    private getSourceLabel(): string {
        const source = this.spec.source;
        if (source.ip) return source.ip;
        if (source.pod) return source.namespace + '/' + source.pod;
        if (this.spec.liveTraffic) {
            return this.getCapturedPacketSrcIP();
        }
        return "";
    }

    private getCapturedPacketSrcIP(): string {
        if (!this.spec.liveTraffic) {
            throw new Error("getCapturedPacketSrcIP called for non live Traceflow");
        }
        const srcIP = this.status?.capturedPacket?.srcIP;
        if (!srcIP) {
            throw new TraceflowResultError("missing src IP in captured packet for live Traceflow");
        }
        return srcIP;
    }

    private getCapturedPacketDstIP(): string {
        if (!this.spec.liveTraffic) {
            throw new Error("getCapturedPacketDstIP called for non live Traceflow");
        }
        const dstIP = this.status?.capturedPacket?.dstIP;
        if (!dstIP) {
            throw new TraceflowResultError("missing dst IP in captured packet for live Traceflow");
        }
        return dstIP;
    }

    private getDestinationPod(): string {
        const dest = this.spec.destination;
        if (dest.pod) return dest.namespace + '/' + dest.pod;
        let pod: string = "";
        this.status.results.forEach(nodeResult => {
            nodeResult.observations.forEach(obs => {
                if (obs.pod) pod = obs.pod;
            });
        });
        return pod;
    }

    private getDestinationLabel(): string {
        const dest = this.spec.destination;
        if (dest.ip) return dest.ip;
        if (dest.service) return dest.namespace + '/' + dest.service;
        const pod = this.getDestinationPod();
        if (pod) return pod;
        if (this.spec.liveTraffic) {
            return this.getCapturedPacketDstIP();
        }
        return "";
    }

    private buildEndpointNode(name: string, label: string): Node {
        const n = new Node(name);
        n.setAttr('style', `"filled,bold"`);
        n.setAttr('label', `"${label}"`);
        n.setAttr('color', grey);
        n.setAttr('fillcolor', lightGrey);
        return n;
    }

    private buildSubgraph(name: string, endpointNode: Node, nodeResult: TraceflowNodeResult, isDst: boolean): [Subgraph, Node] {
        const graph = new Subgraph(name);
        graph.setAttr('style', `"filled,bold"`);
        graph.setAttr('bgcolor', ghostWhite);
        graph.setAttr('label', `"${nodeResult.node}"`);
        const nodes = new Array<Node>();
        if (!isDst) nodes.push(endpointNode);
        nodeResult.observations.forEach((obs, idx) => {
            const nodeName = `${name}_${idx}`;
            const n = new Node(nodeName);
            const label = this.getTraceflowLabel(obs);
            n.setAttr('shape', `"box"`);
            n.setAttr('style', `"rounded,filled,solid"`);
            n.setAttr('label', `"${label}"`);
            n.setAttr('color', grey);
            n.setAttr('fillcolor', lightGrey);
            nodes.push(n);
        });
        if (isDst) nodes.push(endpointNode);
        nodes.forEach(n => graph.addNode(n));
        for (let i = 0; i < nodes.length - 1; i++) {
            graph.addEdge(new Edge(nodes[i].name, nodes[i+1].name));
        }
        if (isDst) return [graph, nodes[0]];
        return [graph, nodes[nodes.length-1]];
    }
}

function TraceflowGraph(props: {spec: TraceflowSpec, status: TraceflowStatus}) {
    const tfSpec = props.spec;
    const tfStatus = props.status;
    const divRef = useRef<HTMLDivElement>(null);
    const { addError } = useAPIError();

    useEffect(() => {
        const graphBuilder = new TraceflowGraphBuilder(tfSpec, tfStatus);
        try {
            renderGraph(graphBuilder.buildGraph());
        } catch(e) {
            console.error(e);
            renderGraph(null);
            if (e instanceof TraceflowResultError) addError(e);
            else throw e;
        }
    }, [addError, tfSpec, tfStatus]);



    function renderGraph(graph: Digraph | null) {
        if (!graph) {
            divRef.current?.replaceChildren();
        } else {
            graphviz(divRef.current).renderDot(graph.asDot());
        }
    }

    return (
        <div ref={divRef}></div>
    );
}

export interface TraceflowResultState {
    spec: TraceflowSpec
    status: TraceflowStatus
}

function TraceflowFailure(props: {spec: TraceflowSpec, status: TraceflowStatus}) {
    const tfStatus = props.status;

    return (
        <CdsAlertGroup status="danger">
            <CdsAlert>Traceflow Failed</CdsAlert>
            <CdsAlert>{tfStatus.reason}</CdsAlert>
        </CdsAlertGroup>
    );
}

export default function TraceflowResult() {
    const { state } = useLocation();

    if (!state || !state.status) {
        return (
            <p>Missing Traceflow Result</p>
        );
    }

    const phase = state.status.phase;

    if (phase !== "Succeeded" && phase !== "Failed") {
        // The API should guarantee that this never happens
        return (
            <p>Invalid Traceflow Phase</p>
        );
    }

    return (
        <div cds-layout="vertical gap:lg">
            <p cds-text="title">Result</p>
            {phase === "Succeeded"
              ? <TraceflowGraph spec={state.spec} status={state.status} />
              : <TraceflowFailure spec={state.spec} status={state.status} />}
        </div>
    );
}
