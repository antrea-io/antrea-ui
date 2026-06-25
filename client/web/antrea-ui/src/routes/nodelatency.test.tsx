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

import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { NodeLatencyStats, nodeLatencyStatsAPI } from '../api/nodelatency';
import NodeLatency from './nodelatency';

vi.mock('../api/nodelatency');
const mockedNodeLatencyStatsAPI = vi.mocked(nodeLatencyStatsAPI, true);

// The heatmap renders an ECharts canvas, which jsdom does not implement; stub it so the
// page-level tests can focus on layout, the stats summary, and the table view.
vi.mock('./nodelatency-heatmap', () => ({
    default: () => <div data-testid="heatmap-stub" />,
}));

afterAll(() => {
    vi.restoreAllMocks();
});
afterEach(() => {
    vi.clearAllMocks();
});

const sendTime = () => new Date(Date.now()).toISOString();
const recvTime = () => new Date(Date.now()).toISOString();

const stats: NodeLatencyStats[] = [
    {
        metadata: { name: 'kind-worker' },
        peerNodeLatencyStats: [
            {
                nodeName: 'kind-control-plane',
                targetIPLatencyStats: [
                    {
                        targetIP: '10.10.0.1',
                        lastMeasuredRTTNanoseconds: 5837000,
                        lastSendTime: sendTime(),
                        lastRecvTime: recvTime(),
                    },
                ],
            },
        ],
    },
];

// node-a fails to reach two peers (2 down egress links) so it is a problem node.
const problemStats: NodeLatencyStats[] = [
    {
        metadata: { name: 'node-a' },
        peerNodeLatencyStats: [
            { nodeName: 'node-b', targetIPLatencyStats: [{ targetIP: '10.0.0.2', lastSendTime: sendTime() }] },
            { nodeName: 'node-c', targetIPLatencyStats: [{ targetIP: '10.0.0.3', lastSendTime: sendTime() }] },
            {
                nodeName: 'node-d',
                targetIPLatencyStats: [{ targetIP: '10.0.0.4', lastMeasuredRTTNanoseconds: 3_000_000, lastSendTime: sendTime(), lastRecvTime: recvTime() }],
            },
        ],
    },
];

describe('NodeLatency', () => {
    test('shows the stats summary and the heatmap by default', async () => {
        mockedNodeLatencyStatsAPI.fetchAll.mockResolvedValueOnce(stats);
        render(<NodeLatency />);
        expect(await screen.findByTestId('heatmap-stub')).toBeInTheDocument();
        expect(screen.getByText('Measured Links')).toBeInTheDocument();
        expect(mockedNodeLatencyStatsAPI.fetchAll).toHaveBeenCalledTimes(1);
    });

    test('table view renders a row per target IP measurement', async () => {
        mockedNodeLatencyStatsAPI.fetchAll.mockResolvedValueOnce(stats);
        render(<NodeLatency />);
        await userEvent.click(await screen.findByRole('button', { name: 'Table' }));
        const row = await screen.findByRole('row', {
            name: new RegExp('kind-worker kind-control-plane 10\\.10\\.0\\.1 5\\.837'),
        });
        expect(within(row).getByRole('cell', { name: '5.837' })).toBeInTheDocument();
    });

    test('shows empty state when no measurements are available', async () => {
        mockedNodeLatencyStatsAPI.fetchAll.mockResolvedValueOnce([]);
        render(<NodeLatency />);
        expect(await screen.findByText(/No Node latency measurements are available/i)).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: 'Table' })).not.toBeInTheDocument();
    });

    test('searching a node opens its detail panel', async () => {
        mockedNodeLatencyStatsAPI.fetchAll.mockResolvedValueOnce(problemStats);
        render(<NodeLatency />);
        const input = await screen.findByLabelText('Search Node');
        await userEvent.type(input, 'node-a');
        expect(await screen.findByText('Node: node-a')).toBeInTheDocument();
        expect(screen.getByText('Egress Links')).toBeInTheDocument();
        expect(screen.getByText('Ingress Links')).toBeInTheDocument();
    });

    test('lists problem nodes and inspecting one opens its detail panel', async () => {
        mockedNodeLatencyStatsAPI.fetchAll.mockResolvedValueOnce(problemStats);
        render(<NodeLatency />);
        expect(await screen.findByText('Problem Nodes (1)')).toBeInTheDocument();
        await userEvent.click(screen.getByRole('button', { name: 'Inspect' }));
        expect(await screen.findByText('Node: node-a')).toBeInTheDocument();
    });

    test('table view renders N/A for a missing RTT', async () => {
        mockedNodeLatencyStatsAPI.fetchAll.mockResolvedValueOnce([
            {
                metadata: { name: 'kind-worker' },
                peerNodeLatencyStats: [
                    {
                        nodeName: 'kind-worker2',
                        targetIPLatencyStats: [{ targetIP: '10.10.2.1' }],
                    },
                ],
            },
        ]);
        render(<NodeLatency />);
        await userEvent.click(await screen.findByRole('button', { name: 'Table' }));
        const row = await screen.findByRole('row', { name: /kind-worker2 10\.10\.2\.1 N\/A/ });
        expect(within(row).getByRole('cell', { name: 'N/A' })).toBeInTheDocument();
    });
});
