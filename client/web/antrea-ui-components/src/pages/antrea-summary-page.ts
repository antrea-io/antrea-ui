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

import { html } from 'lit';
import { state } from 'lit/decorators.js';
import { pageStyles } from '../lib/styles.js';
import { apiFetchJSON, APIError } from '../lib/api.js';
import { accessSummary, can, canNonResource, GATE_AGENT_INFO_LIST, GATE_CONTROLLER_INFO_GET, GATE_FEATUREGATES } from '../lib/access-api.js';
import type { AccessSummary } from '../lib/access-api.js';
import { SessionAwarePage } from '../lib/session-aware-page.js';
import { renderStaticTable } from '../lib/render-table.js';
import '../antrea-card';
import '../antrea-alert';

// ── Types (mirror antrea-ui/src/api/info.tsx) ────────────────────────────────

interface K8sRef { namespace?: string; name: string; }
interface Condition { type: string; status: string; lastHeartbeatTime: string; reason: string; message: string; }

interface ControllerInfo {
    metadata: { name: string };
    version?: string;
    podRef?: K8sRef;
    nodeRef?: K8sRef;
    connectedAgentNum?: number;
    controllerConditions?: Condition[];
}

interface AgentInfo {
    metadata: { name: string };
    version?: string;
    podRef?: K8sRef;
    nodeRef?: K8sRef;
    nodeSubnets?: string[];
    ovsInfo?: { version?: string };
    localPodNum?: number;
    agentConditions?: Condition[];
}

interface FeatureGate { component: string; name: string; status: string; version: string; }

// ── Helpers ──────────────────────────────────────────────────────────────────

function refStr(ref: K8sRef | undefined): string {
    if (!ref) return 'Unknown';
    return ref.namespace ? `${ref.namespace}/${ref.name}` : ref.name;
}

function conditionInfo(conditions: Condition[] | undefined, type: string): [string, string] {
    const c = conditions?.find(c => c.type === type);
    if (!c) return ['False', 'None'];
    return [c.status, new Date(c.lastHeartbeatTime).toLocaleString()];
}

// ── Component ─────────────────────────────────────────────────────────────────

export class AntreaSummaryPage extends SessionAwarePage {
    static styles = pageStyles;

    @state() private _controller?: ControllerInfo;
    @state() private _agents?: AgentInfo[];
    @state() private _controllerFG?: FeatureGate[];
    @state() private _agentFG?: FeatureGate[];
    @state() private _loading = true;
    // Set when at least one card was skipped because the user is not permitted to read it: the
    // gate said no, or the request came back 403 anyway.
    @state() private _someCardsForbidden = false;
    // Set when at least one card the user *is* permitted to read failed to load for any other
    // reason. Reported separately, and with the error text: telling someone their account lacks
    // a permission it actually has sends them to the wrong place entirely.
    @state() private _cardErrors: string[] = [];

    // Bumped before each _load() and captured per-call, so a response from a superseded call
    // can't overwrite state after a newer one has already resolved.
    private _loadGeneration = 0;

    protected override async onSessionReady() {
        const generation = ++this._loadGeneration;
        this._loading = true;
        this._someCardsForbidden = false;
        this._cardErrors = [];
        this._controller = undefined;
        this._agents = undefined;
        this._controllerFG = undefined;
        this._agentFG = undefined;

        // A stale or unreachable summary (fetch failure, fail-open incomplete) is treated as
        // "permitted": the route guard already prevents reaching this page when nothing is
        // permitted, and any 403 that slips through anyway is caught per-card below.
        let summary: AccessSummary | null;
        try {
            summary = await accessSummary();
        } catch {
            summary = null;
        }
        if (generation !== this._loadGeneration) return;
        await this._load(generation, summary);
    }

    private async _load(generation: number, summary: AccessSummary | null) {
        const canController = can(summary, GATE_CONTROLLER_INFO_GET);
        const canAgents = can(summary, GATE_AGENT_INFO_LIST);
        const canFeatureGates = canNonResource(summary, GATE_FEATUREGATES);

        if (!canController) this._someCardsForbidden = true;
        if (!canAgents) this._someCardsForbidden = true;
        if (!canFeatureGates) this._someCardsForbidden = true;

        const [controllerResult, agentsResult, featureGatesResult] = await Promise.allSettled([
            canController
                ? apiFetchJSON<ControllerInfo>('k8s/apis/crd.antrea.io/v1beta1/antreacontrollerinfos/antrea-controller')
                : Promise.reject(new Error('skipped: no permission')),
            canAgents
                ? apiFetchJSON<{ items: AgentInfo[] }>('k8s/apis/crd.antrea.io/v1beta1/antreaagentinfos')
                : Promise.reject(new Error('skipped: no permission')),
            canFeatureGates
                ? apiFetchJSON<FeatureGate[]>('featuregates')
                : Promise.reject(new Error('skipped: no permission')),
        ]);
        if (generation !== this._loadGeneration) return;

        for (const result of [controllerResult, agentsResult, featureGatesResult]) {
            if (result.status === 'rejected' && this.isSessionExpiredError(result.reason)) {
                this.dispatchSessionExpired();
                return;
            }
        }

        if (controllerResult.status === 'fulfilled') {
            this._controller = controllerResult.value;
        } else if (canController) {
            this._recordCardFailure('Controller', controllerResult.reason);
        }

        if (agentsResult.status === 'fulfilled') {
            this._agents = agentsResult.value.items;
        } else if (canAgents) {
            this._recordCardFailure('Agents', agentsResult.reason);
        }

        if (featureGatesResult.status === 'fulfilled') {
            this._controllerFG = featureGatesResult.value.filter(fg => fg.component === 'controller');
            this._agentFG = featureGatesResult.value.filter(fg => fg.component === 'agent');
        } else if (canFeatureGates) {
            this._recordCardFailure('Feature Gates', featureGatesResult.reason);
        }

        this._loading = false;
    }

    /** Classifies a failed card fetch the gate had allowed. A 403 means the summary was wrong
     * (stale, incomplete, or fail-open) and the card really is forbidden; anything else is a
     * genuine error and must be surfaced as one, with its message. */
    private _recordCardFailure(label: string, reason: unknown) {
        if (reason instanceof APIError && reason.code === 403) {
            this._someCardsForbidden = true;
            return;
        }
        const message = reason instanceof Error ? reason.message : String(reason);
        this._cardErrors = [...this._cardErrors, `${label}: ${message}`];
    }

    override render() {
        if (this._loading) {
            return html`<main><div class="loading-row"><span class="spinner"></span><span>Loading…</span></div></main>`;
        }

        const ctrlHealth = this._controller
            ? conditionInfo(this._controller.controllerConditions, 'ControllerHealthy')
            : undefined;

        return html`
            <main>
                <div class="page-layout">
                    <p class="page-title">Summary</p>
                    ${this._someCardsForbidden ? html`
                        <antrea-alert status="info">
                            Some information is not shown because your account does not have permission to view it.
                        </antrea-alert>
                    ` : ''}
                    ${this._cardErrors.length > 0 ? html`
                        <antrea-alert status="danger">
                            ${this._cardErrors.join('; ')}
                        </antrea-alert>
                    ` : ''}

                    ${this._controller && ctrlHealth ? html`
                        <antrea-card heading="Controller">
                            ${renderStaticTable(
                                ['Name', 'Version', 'Pod Name', 'Node Name', 'Connected Agents', 'Healthy', 'Last Heartbeat'],
                                [this._controller],
                                c => [
                                    c.metadata.name,
                                    c.version ?? 'Unknown',
                                    refStr(c.podRef),
                                    refStr(c.nodeRef),
                                    (c.connectedAgentNum ?? 0).toString(),
                                    ctrlHealth[0],
                                    ctrlHealth[1],
                                ],
                            )}
                        </antrea-card>
                    ` : ''}

                    ${this._agents ? html`
                        <antrea-card heading="Agents">
                            ${renderStaticTable(
                                ['Name', 'Version', 'Pod Name', 'Node Name', 'Local Pods', 'Node Subnets', 'OVS Version', 'Healthy', 'Last Heartbeat'],
                                this._agents,
                                a => {
                                    const [healthy, heartbeat] = conditionInfo(a.agentConditions, 'AgentHealthy');
                                    return [
                                        a.metadata.name,
                                        a.version ?? 'Unknown',
                                        refStr(a.podRef),
                                        refStr(a.nodeRef),
                                        (a.localPodNum ?? 0).toString(),
                                        a.nodeSubnets?.join(',') ?? 'None',
                                        a.ovsInfo?.version ?? 'Unknown',
                                        healthy,
                                        heartbeat,
                                    ];
                                },
                            )}
                        </antrea-card>
                    ` : ''}

                    ${this._controllerFG ? html`
                        <antrea-card heading="Controller Feature Gates">
                            ${renderStaticTable(
                                ['Name', 'Status', 'Version'],
                                this._controllerFG,
                                fg => [fg.name, fg.status, fg.version],
                            )}
                        </antrea-card>
                    ` : ''}

                    ${this._agentFG ? html`
                        <antrea-card heading="Agent Feature Gates">
                            ${renderStaticTable(
                                ['Name', 'Status', 'Version'],
                                this._agentFG,
                                fg => [fg.name, fg.status, fg.version],
                            )}
                        </antrea-card>
                    ` : ''}
                </div>
            </main>
        `;
    }
}

customElements.define('antrea-summary-page', AntreaSummaryPage);

declare global {
    interface HTMLElementTagNameMap { 'antrea-summary-page': AntreaSummaryPage; }
}
