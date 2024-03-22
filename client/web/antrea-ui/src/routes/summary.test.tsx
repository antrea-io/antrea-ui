/**
 * Copyright 2023 Antrea Authors.
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

import { render, screen, within } from '@testing-library/react';
import { AgentInfo, ControllerInfo, K8sRef, Condition, agentInfoAPI, controllerInfoAPI } from '../api/info';
import { FeatureGate, featureGatesAPI } from '../api/featuregates';
import Summary from './summary';

vi.mock('../api/info');
const mockedAgentInfoAPI = vi.mocked(agentInfoAPI, true);
const mockedControllerInfoAPI = vi.mocked(controllerInfoAPI, true);
vi.mock('../api/featuregates');
const mockedFeatureGatesAPI = vi.mocked(featureGatesAPI, true);

afterAll(() => {
    vi.restoreAllMocks();
});
afterEach(() => {
    vi.clearAllMocks();
});

function makeMetadata(name: string) {
    return {
        name: name,
    };
}

function makePodRef(name: string): K8sRef {
    return {
        namespace: 'kube-system',
        name: name,
    };
}

function makeNodeRef(name: string): K8sRef {
    return {
        name: name,
    };
}

function makeCondition(type: string, status: string, date: Date): Condition {
    return {
        type: type,
        status: status,
        lastHeartbeatTime: date.toUTCString(),
        reason: "",
        message: "",
    };
}

function arrayFrequencies<T>(data: T[]): Map<T, number> {
    const counts = new Map<T, number>();
    data.forEach(d => counts.set(d, (counts.get(d)??0) + 1));
    return counts;
}

function checkControllerInfo(data: string[]) {
    const section = screen.getByRole('region', { name: /^controller$/i });
    expect(within(section).getByText('Controller')).toBeInTheDocument();
    if (!data) {
        expect(within(section).getAllByRole('row')).toHaveLength(1);
        return;
    }
    // 2 rows: title row and controller row
    expect(within(section).getAllByRole('row')).toHaveLength(2);
    // name of the row is the contents of all cells, space-separated
    const row = within(section).getByRole('row', { name: data.join(' ') });
    // some cells may have the same content / name, which is why we check that the count is correct
    arrayFrequencies(data).forEach((count, c) => expect(within(row).getAllByRole('cell', { name: c })).toHaveLength(count));
}

function checkAgentInfos(data: string[][] | undefined) {
    const section = screen.getByRole('region', { name: /^agents$/i });
    expect(within(section).getByText('Agents')).toBeInTheDocument();
    if (!data) {
        expect(within(section).getAllByRole('row')).toHaveLength(1);
        return;
    }
    data.forEach(data => {
        const row = within(section).getByRole('row', { name: data.join(' ') });
        // some cells may have the same content / name, which is why we check that the count is correct
        arrayFrequencies(data).forEach((count, c) => expect(within(row).getAllByRole('cell', { name: c })).toHaveLength(count));
    });
}

function checkFeatureGates(component: string, data: string[][] | undefined) {
    const re = new RegExp('^' + component + ' feature gates$', 'i');
    const section = screen.getByRole('region', { name: re });
    expect(within(section).getByText(component + ' Feature Gates')).toBeInTheDocument();
    if (!data) {
        expect(within(section).getAllByRole('row')).toHaveLength(1);
        return;
    }
    data.forEach(data => {
        const row = within(section).getByRole('row', { name: data.join(' ') });
        // some cells may have the same content / name, which is why we check that the count is correct
        arrayFrequencies(data).forEach((count, c) => expect(within(row).getAllByRole('cell', { name: c })).toHaveLength(count));
    });
}

function makeControllerInfo(numAgents: number, conditions: Condition[]): ControllerInfo {
    return {
        metadata: makeMetadata('antrea-controller'),
        version: 'v1.0.0',
        podRef: makePodRef('antrea-controller'),
        nodeRef: makeNodeRef('nodeA'),
        connectedAgentNum: numAgents,
        controllerConditions: conditions,
    };
}

function makeAgentInfo(name: string, nodeName: string, numPods: number, nodeSubnets: string[], conditions: Condition[]): AgentInfo {
    return {
        metadata: makeMetadata(name),
        version: 'v1.0.0',
        podRef: makePodRef(name),
        nodeRef: makeNodeRef(nodeName),
        nodeSubnets: nodeSubnets,
        ovsInfo: {
            version: '2.17.5',
        },
        localPodNum: numPods,
        agentConditions: conditions,
    };
}

describe('Summary', () => {
    const d1 = new Date();

    interface testCase {
        name: string
        controllerInfo: ControllerInfo
        expectedControllerData: string[]
        agentInfo?: AgentInfo[]
        expectedAgentData?: string[][]
    }

    const controller = makeControllerInfo(2, [makeCondition('ControllerHealthy', 'True', d1)]);
    const controllerData = ['antrea-controller', 'v1.0.0', 'kube-system/antrea-controller', 'nodeA', '2', 'True', d1.toLocaleString()];
    const agent1 = makeAgentInfo('antrea-agent-1', 'nodeA', 0, ['10.0.1.0/24', 'fd02::01/48'], [makeCondition('AgentHealthy', 'True', d1)]);
    const agentData1 = ['antrea-agent-1', 'v1.0.0', 'kube-system/antrea-agent-1', 'nodeA', '0', '10.0.1.0/24,fd02::01/48', '2.17.5', 'True', d1.toLocaleString()];
    const agent2 = makeAgentInfo('antrea-agent-2', 'nodeB', 3, ['10.0.2.0/24', 'fd02::02/48'], [makeCondition('AgentHealthy', 'True', d1)]);
    const agentData2 = ['antrea-agent-2', 'v1.0.0', 'kube-system/antrea-agent-2', 'nodeB', '3', '10.0.2.0/24,fd02::02/48', '2.17.5', 'True', d1.toLocaleString()];
    const featureGates: FeatureGate[] = [
        {
            component: 'controller',
            name: 'AntreaPolicy',
            status: 'Enabled',
            version: 'BETA',
        },
        {
            component: 'agent',
            name: 'AntreaProxy',
            status: 'Enabled',
            version: 'BETA',
        },
    ];
    const featureGatesControllerData = [['AntreaPolicy', 'Enabled', 'BETA']];
    const featureGatesAgentData = [['AntreaProxy', 'Enabled', 'BETA']];

    const testCases: testCase[] = [
        {
            name: 'controller + 2 agents',
            controllerInfo: controller,
            expectedControllerData: controllerData,
            agentInfo: [agent1, agent2],
            expectedAgentData: [agentData1, agentData2],
        },
        {
            name: 'no agents',
            controllerInfo: makeControllerInfo(0, [makeCondition('ControllerHealthy', 'True', d1)]),
            expectedControllerData: ['antrea-controller', 'v1.0.0', 'kube-system/antrea-controller', 'nodeA', '0', 'True', d1.toLocaleString()],
        },
        {
            name: 'missing condition',
            controllerInfo: makeControllerInfo(0, []),
            expectedControllerData: ['antrea-controller', 'v1.0.0', 'kube-system/antrea-controller', 'nodeA', '0', 'False', 'None'],
        },
        {
            name: 'bad heartbeat time',
            controllerInfo: makeControllerInfo(0, [{
                type: 'ControllerHealthy',
                status: 'True',
                lastHeartbeatTime: 'missing',
                reason: "",
                message: "",
            }]),
            expectedControllerData: ['antrea-controller', 'v1.0.0', 'kube-system/antrea-controller', 'nodeA', '0', 'True', 'Invalid Date'],
        },
        {
            name: 'missing controller fields',
            controllerInfo: {
                metadata: makeMetadata('antrea-controller'),
            },
            expectedControllerData: ['antrea-controller', 'Unknown', 'Unknown', 'Unknown', '0', 'False', 'None'],
        },
        {
            name: 'missing agent fields',
            controllerInfo: controller,
            expectedControllerData: controllerData,
            agentInfo: [{
                metadata: makeMetadata('antrea-agent-1'),
            }],
            expectedAgentData: [
                ['antrea-agent-1', 'Unknown', 'Unknown', 'Unknown', '0', 'None', 'Unknown', 'False', 'None'],
            ],
        },
    ];

    test.each<testCase>(testCases)('$name', async (tc: testCase) => {
        mockedControllerInfoAPI.fetch.mockResolvedValueOnce(tc.controllerInfo);
        mockedAgentInfoAPI.fetchAll.mockResolvedValueOnce(tc.agentInfo || []);
        mockedFeatureGatesAPI.fetch.mockResolvedValueOnce(featureGates);
        render(<Summary />);
        expect(await screen.findByText('Controller')).toBeInTheDocument();
        expect(await screen.findByText('Agents')).toBeInTheDocument();
        expect(await screen.findByText('Controller Feature Gates')).toBeInTheDocument();
        expect(await screen.findByText('Agent Feature Gates')).toBeInTheDocument();
        // By the time the assertions above are verified, API calls have been made
        expect(mockedControllerInfoAPI.fetch).toHaveBeenCalledTimes(1);
        expect(mockedAgentInfoAPI.fetchAll).toHaveBeenCalledTimes(1);
        expect(mockedFeatureGatesAPI.fetch).toHaveBeenCalledTimes(1);
        checkControllerInfo(tc.expectedControllerData);
        checkAgentInfos(tc.expectedAgentData);
        checkFeatureGates('Controller', featureGatesControllerData);
        checkFeatureGates('Agent', featureGatesAgentData);
    });
});
