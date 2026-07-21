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
import { apiFetchJSON } from '../lib/api.js';
import { TokenAwarePage } from '../lib/token-aware-page.js';
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

export class AntreaSummaryPage extends TokenAwarePage {
    static styles = pageStyles;

    @state() private _controller?: ControllerInfo;
    @state() private _agents?: AgentInfo[];
    @state() private _controllerFG?: FeatureGate[];
    @state() private _agentFG?: FeatureGate[];
    @state() private _loading = true;
    @state() private _error = '';

    // onTokenReady() fires on every token change, including a silent refresh mid-load — bumped
    // before each _load() and captured per-call, so a response from a superseded call can't
    // overwrite state after a newer one has already resolved.
    private _loadGeneration = 0;

    protected override onTokenReady() {
        this._load();
    }

    private async _load() {
        const generation = ++this._loadGeneration;
        this._loading = true;
        this._error = '';
        try {
            const [controller, agentsResp, featureGates] = await Promise.all([
                apiFetchJSON<ControllerInfo>(
                    'k8s/apis/crd.antrea.io/v1beta1/antreacontrollerinfos/antrea-controller',
                    this.token,
                ),
                apiFetchJSON<{ items: AgentInfo[] }>(
                    'k8s/apis/crd.antrea.io/v1beta1/antreaagentinfos',
                    this.token,
                ),
                apiFetchJSON<FeatureGate[]>('featuregates', this.token),
            ]);
            if (generation !== this._loadGeneration) return;
            this._controller = controller;
            this._agents = agentsResp.items;
            this._controllerFG = featureGates.filter(fg => fg.component === 'controller');
            this._agentFG = featureGates.filter(fg => fg.component === 'agent');
            this._loading = false;
        } catch (e) {
            if (generation !== this._loadGeneration) return;
            if (this.isSessionExpiredError(e)) {
                this.dispatchSessionExpired();
                // Stay in the loading state: no controller/agent data was set, so falling
                // through to render() would crash dereferencing it. The host is expected to
                // refresh the token and re-set it, which re-triggers onTokenReady() -> _load().
                return;
            }
            this._error = e instanceof Error ? e.message : String(e);
            this._loading = false;
            this.dispatchEvent(new CustomEvent('antrea-error', { detail: { message: this._error }, bubbles: true, composed: true }));
        }
    }

    override render() {
        if (this._loading) {
            return html`<main><div class="loading-row"><span class="spinner"></span><span>Loading…</span></div></main>`;
        }
        if (this._error) {
            return html`<main><antrea-alert status="danger">${this._error}</antrea-alert></main>`;
        }

        const controller = this._controller!;
        const [ctrlHealthy, ctrlHeartbeat] = conditionInfo(controller.controllerConditions, 'ControllerHealthy');

        return html`
            <main>
                <div class="page-layout">
                    <p class="page-title">Summary</p>

                    <antrea-card heading="Controller">
                        ${renderStaticTable(
                            ['Name', 'Version', 'Pod Name', 'Node Name', 'Connected Agents', 'Healthy', 'Last Heartbeat'],
                            [controller],
                            c => [
                                c.metadata.name,
                                c.version ?? 'Unknown',
                                refStr(c.podRef),
                                refStr(c.nodeRef),
                                (c.connectedAgentNum ?? 0).toString(),
                                ctrlHealthy,
                                ctrlHeartbeat,
                            ],
                        )}
                    </antrea-card>

                    <antrea-card heading="Agents">
                        ${renderStaticTable(
                            ['Name', 'Version', 'Pod Name', 'Node Name', 'Local Pods', 'Node Subnets', 'OVS Version', 'Healthy', 'Last Heartbeat'],
                            this._agents!,
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

                    <antrea-card heading="Controller Feature Gates">
                        ${renderStaticTable(
                            ['Name', 'Status', 'Version'],
                            this._controllerFG!,
                            fg => [fg.name, fg.status, fg.version],
                        )}
                    </antrea-card>

                    <antrea-card heading="Agent Feature Gates">
                        ${renderStaticTable(
                            ['Name', 'Status', 'Version'],
                            this._agentFG!,
                            fg => [fg.name, fg.status, fg.version],
                        )}
                    </antrea-card>
                </div>
            </main>
        `;
    }
}

customElements.define('antrea-summary-page', AntreaSummaryPage);

declare global {
    interface HTMLElementTagNameMap { 'antrea-summary-page': AntreaSummaryPage; }
}
