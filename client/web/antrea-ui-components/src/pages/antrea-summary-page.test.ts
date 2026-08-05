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

import { afterEach, describe, expect, test, vi } from 'vitest';
import './antrea-summary-page';
import type { AntreaSummaryPage } from './antrea-summary-page';
import { resetAccessSummary } from '../lib/access-api';
import type { AccessSummary } from '../lib/access-api';

interface K8sRef { namespace?: string; name: string; }
interface Condition { type: string; status: string; lastHeartbeatTime: string; reason: string; message: string; }

function jsonResponse(body: unknown, status = 200): Response {
    return new Response(JSON.stringify(body), { status });
}

function makeMetadata(name: string) {
    return { name };
}

function makePodRef(name: string): K8sRef {
    return { namespace: 'kube-system', name };
}

function makeNodeRef(name: string): K8sRef {
    return { name };
}

function makeCondition(type: string, status: string, date: Date): Condition {
    return { type, status, lastHeartbeatTime: date.toUTCString(), reason: '', message: '' };
}

function makeControllerInfo(numAgents: number, conditions: Condition[]) {
    return {
        metadata: makeMetadata('antrea-controller'),
        version: 'v1.0.0',
        podRef: makePodRef('antrea-controller'),
        nodeRef: makeNodeRef('nodeA'),
        connectedAgentNum: numAgents,
        controllerConditions: conditions,
    };
}

function makeAgentInfo(name: string, nodeName: string, numPods: number, nodeSubnets: string[], conditions: Condition[]) {
    return {
        metadata: makeMetadata(name),
        version: 'v1.0.0',
        podRef: makePodRef(name),
        nodeRef: makeNodeRef(nodeName),
        nodeSubnets,
        ovsInfo: { version: '2.17.5' },
        localPodNum: numPods,
        agentConditions: conditions,
    };
}

const featureGates = [
    { component: 'controller', name: 'AntreaPolicy', status: 'Enabled', version: 'BETA' },
    { component: 'agent', name: 'AntreaProxy', status: 'Enabled', version: 'BETA' },
];

function fullAccessSummary(): AccessSummary {
    return {
        username: 'alice',
        groups: [],
        clusterAdmin: true,
        rules: {
            resourceRules: [{ apiGroups: ['*'], resources: ['*'], verbs: ['*'] }],
            nonResourceRules: [{ nonResourceURLs: ['*'], verbs: ['*'] }],
            incomplete: false,
        },
        namespaces: ['*'],
    };
}

let el: AntreaSummaryPage | undefined;

afterEach(() => {
    el?.remove();
    el = undefined;
    vi.unstubAllGlobals();
    resetAccessSummary();
});

/** Mounts the page against an arbitrary fetch handler, for the cases mount()'s fixed routing
 * cannot express (per-URL status codes). */
async function mountWith(handler: (url: string) => Promise<Response>): Promise<AntreaSummaryPage> {
    vi.stubGlobal('fetch', vi.fn(handler));
    el = document.createElement('antrea-summary-page') as AntreaSummaryPage;
    document.body.appendChild(el);
    await el.updateComplete;
    await new Promise(r => setTimeout(r, 0));
    await new Promise(r => setTimeout(r, 0));
    await el.updateComplete;
    return el;
}

async function mount(
    controllerInfo: unknown,
    agentInfo: unknown[] = [],
    summary: AccessSummary | null = fullAccessSummary(),
): Promise<AntreaSummaryPage> {
    return mountWith(async (url: string) => {
        if (url.endsWith('/access-summary')) {
            return summary === null ? new Response('', { status: 500 }) : jsonResponse(summary);
        }
        if (url.endsWith('/antreacontrollerinfos/antrea-controller')) return jsonResponse(controllerInfo);
        if (url.endsWith('/antreaagentinfos')) return jsonResponse({ items: agentInfo });
        if (url.endsWith('/featuregates')) return jsonResponse(featureGates);
        throw new Error(`unexpected fetch to ${url}`);
    });
}

function tableRows(page: AntreaSummaryPage, heading: string): string[][] {
    const table = page.shadowRoot!.querySelector(`antrea-card[heading="${heading}"] table`);
    return Array.from(table?.querySelectorAll('tbody tr') ?? []).map(
        row => Array.from(row.querySelectorAll('td')).map(cell => cell.textContent ?? ''),
    );
}

function hasCard(page: AntreaSummaryPage, heading: string): boolean {
    return page.shadowRoot!.querySelector(`antrea-card[heading="${heading}"]`) !== null;
}

function alertText(page: AntreaSummaryPage, status: 'info' | 'danger'): string | null {
    const alert = page.shadowRoot!.querySelector(`antrea-alert[status="${status}"]`);
    return alert === null ? null : (alert.textContent ?? '').trim();
}

describe('AntreaSummaryPage', () => {
    const d1 = new Date();

    test('controller + 2 agents', async () => {
        const controller = makeControllerInfo(2, [makeCondition('ControllerHealthy', 'True', d1)]);
        const agent1 = makeAgentInfo('antrea-agent-1', 'nodeA', 0, ['10.0.1.0/24', 'fd02::01/48'], [makeCondition('AgentHealthy', 'True', d1)]);
        const agent2 = makeAgentInfo('antrea-agent-2', 'nodeB', 3, ['10.0.2.0/24', 'fd02::02/48'], [makeCondition('AgentHealthy', 'True', d1)]);

        const page = await mount(controller, [agent1, agent2]);

        expect(tableRows(page, 'Controller')).toEqual([
            ['antrea-controller', 'v1.0.0', 'kube-system/antrea-controller', 'nodeA', '2', 'True', d1.toLocaleString()],
        ]);
        expect(tableRows(page, 'Agents')).toEqual([
            ['antrea-agent-1', 'v1.0.0', 'kube-system/antrea-agent-1', 'nodeA', '0', '10.0.1.0/24,fd02::01/48', '2.17.5', 'True', d1.toLocaleString()],
            ['antrea-agent-2', 'v1.0.0', 'kube-system/antrea-agent-2', 'nodeB', '3', '10.0.2.0/24,fd02::02/48', '2.17.5', 'True', d1.toLocaleString()],
        ]);
        expect(tableRows(page, 'Controller Feature Gates')).toEqual([['AntreaPolicy', 'Enabled', 'BETA']]);
        expect(tableRows(page, 'Agent Feature Gates')).toEqual([['AntreaProxy', 'Enabled', 'BETA']]);
    });

    test('no agents', async () => {
        const controller = makeControllerInfo(0, [makeCondition('ControllerHealthy', 'True', d1)]);
        const page = await mount(controller);

        expect(tableRows(page, 'Controller')).toEqual([
            ['antrea-controller', 'v1.0.0', 'kube-system/antrea-controller', 'nodeA', '0', 'True', d1.toLocaleString()],
        ]);
        expect(tableRows(page, 'Agents')).toEqual([]);
    });

    test('missing condition', async () => {
        const controller = makeControllerInfo(0, []);
        const page = await mount(controller);

        expect(tableRows(page, 'Controller')).toEqual([
            ['antrea-controller', 'v1.0.0', 'kube-system/antrea-controller', 'nodeA', '0', 'False', 'None'],
        ]);
    });

    test('bad heartbeat time', async () => {
        const controller = makeControllerInfo(0, [{
            type: 'ControllerHealthy', status: 'True', lastHeartbeatTime: 'missing', reason: '', message: '',
        }]);
        const page = await mount(controller);

        expect(tableRows(page, 'Controller')).toEqual([
            ['antrea-controller', 'v1.0.0', 'kube-system/antrea-controller', 'nodeA', '0', 'True', 'Invalid Date'],
        ]);
    });

    test('missing controller fields', async () => {
        const page = await mount({ metadata: makeMetadata('antrea-controller') });

        expect(tableRows(page, 'Controller')).toEqual([
            ['antrea-controller', 'Unknown', 'Unknown', 'Unknown', '0', 'False', 'None'],
        ]);
    });

    test('missing agent fields', async () => {
        const controller = makeControllerInfo(2, [makeCondition('ControllerHealthy', 'True', d1)]);
        const page = await mount(controller, [{ metadata: makeMetadata('antrea-agent-1') }]);

        expect(tableRows(page, 'Agents')).toEqual([
            ['antrea-agent-1', 'Unknown', 'Unknown', 'Unknown', '0', 'None', 'Unknown', 'False', 'None'],
        ]);
    });
});

describe('AntreaSummaryPage — per-card degradation', () => {
    test('a card the user cannot read is skipped, not fetched, and a note is shown', async () => {
        const controller = makeControllerInfo(0, []);
        const summary: AccessSummary = {
            ...fullAccessSummary(),
            rules: {
                resourceRules: [{ apiGroups: ['crd.antrea.io'], resources: ['antreacontrollerinfos'], verbs: ['get'] }],
                nonResourceRules: [{ nonResourceURLs: ['/featuregates'], verbs: ['get'] }],
                incomplete: false,
            },
        };
        const page = await mount(controller, [], summary);

        expect(hasCard(page, 'Controller')).toBe(true);
        expect(hasCard(page, 'Agents')).toBe(false);
        expect(page.shadowRoot!.querySelector('antrea-alert[status="info"]')).not.toBeNull();
    });

    test('one card 403ing degrades that card while the others still render', async () => {
        const controller = makeControllerInfo(1, [makeCondition('ControllerHealthy', 'True', new Date())]);
        const page = await mountWith(async (url: string) => {
            if (url.endsWith('/access-summary')) return jsonResponse(fullAccessSummary());
            if (url.endsWith('/antreacontrollerinfos/antrea-controller')) return jsonResponse(controller);
            if (url.endsWith('/antreaagentinfos')) return new Response('forbidden', { status: 403 });
            if (url.endsWith('/featuregates')) return jsonResponse(featureGates);
            throw new Error(`unexpected fetch to ${url}`);
        });

        expect(hasCard(page, 'Controller')).toBe(true);
        expect(hasCard(page, 'Agents')).toBe(false);
        expect(hasCard(page, 'Controller Feature Gates')).toBe(true);
        expect(alertText(page, 'info')).not.toBeNull();
        // A 403 is a permissions problem, not a failure: no danger alert.
        expect(alertText(page, 'danger')).toBeNull();
    });

    test('access-summary fetch failure fails open: every card is fetched', async () => {
        const controller = makeControllerInfo(0, []);
        const page = await mount(controller, [], null);

        expect(hasCard(page, 'Controller')).toBe(true);
        expect(hasCard(page, 'Agents')).toBe(true);
        expect(page.shadowRoot!.querySelector('antrea-alert[status="info"]')).toBeNull();
    });
});

describe('AntreaSummaryPage — errors', () => {
    test('a non-403 card failure shows a danger alert carrying the error, not the permissions note', async () => {
        // The scenario the permissions note must not claim: the backend is down, so the
        // access-summary fetch fails too, every gate fails open, and every card errors out.
        const page = await mountWith(async () => new Response('backend exploded', { status: 500 }));

        expect(hasCard(page, 'Controller')).toBe(false);
        expect(alertText(page, 'info')).toBeNull();
        expect(alertText(page, 'danger')).toContain('backend exploded');
    });

    test('a 403 on one card and a 500 on another show both alerts', async () => {
        const controller = makeControllerInfo(1, [makeCondition('ControllerHealthy', 'True', new Date())]);
        const page = await mountWith(async (url: string) => {
            if (url.endsWith('/access-summary')) return jsonResponse(fullAccessSummary());
            if (url.endsWith('/antreacontrollerinfos/antrea-controller')) return jsonResponse(controller);
            if (url.endsWith('/antreaagentinfos')) return new Response('forbidden', { status: 403 });
            if (url.endsWith('/featuregates')) return new Response('boom', { status: 500 });
            throw new Error(`unexpected fetch to ${url}`);
        });

        expect(hasCard(page, 'Controller')).toBe(true);
        expect(alertText(page, 'info')).not.toBeNull();
        expect(alertText(page, 'danger')).toContain('boom');
        // The failing card is named, since only some of them failed.
        expect(alertText(page, 'danger')).toContain('Feature Gates');
    });

    test('a 401 response dispatches antrea-session-expired', async () => {
        // Each concurrent apiFetchJSON() call reads its own Response body, so the mock must
        // hand out a fresh Response per call rather than one shared instance.
        vi.stubGlobal('fetch', vi.fn(async () => new Response('', { status: 401 })));
        el = document.createElement('antrea-summary-page') as AntreaSummaryPage;
        const onSessionExpired = vi.fn();
        el.addEventListener('antrea-session-expired', onSessionExpired);
        document.body.appendChild(el);
        await el.updateComplete;
        await new Promise(r => setTimeout(r, 0));
        await new Promise(r => setTimeout(r, 0));

        expect(onSessionExpired).toHaveBeenCalledTimes(1);
    });
});
