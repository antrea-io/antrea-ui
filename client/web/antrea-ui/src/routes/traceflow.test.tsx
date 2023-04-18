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

import React from 'react';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { mockIntersectionObserver } from 'jsdom-testing-mocks';
import Traceflow from './traceflow';
import { traceflowAPI, TraceflowSpec, TraceflowStatus } from '../api/traceflow';

// required by Clarity
mockIntersectionObserver();

jest.mock('../api/traceflow');
const mockedTraceflowAPI = jest.mocked(traceflowAPI, true);

const mockNavigate = jest.fn();
jest.mock('react-router-dom', () => ({
    ...jest.requireActual('react-router-dom') as any,
    useNavigate: () => mockNavigate,
}));

afterAll(() => {
    jest.restoreAllMocks();
});
afterEach(() => {
    jest.resetAllMocks();
});

interface testInputs {
    liveTraffic?: boolean
    droppedOnly?: boolean
    proto?: string
    ipv6?: boolean
    srcNamespace?: string
    src?: string
    srcPort?: number
    destinationType?: string
    dstNamespace?: string
    dst?: string
    dstPort?: number
    timeout?: number
    tcpFlags?: number
}

async function inputsToEvents(inputs: testInputs) {
    if (inputs.liveTraffic) userEvent.click(await screen.findByLabelText('Live Traffic'));
    if (inputs.droppedOnly) userEvent.click(await screen.findByLabelText('Dropped Traffic Only'));
    if (inputs.proto) userEvent.selectOptions(await screen.findByLabelText('Protocol'), inputs.proto);
    if (inputs.ipv6) userEvent.click(await screen.findByLabelText('Use IPv6'));
    if (inputs.srcNamespace) {
        const srcNamespace = await screen.findByLabelText('Source Namespace');
        // clear needs to be called first, to remove the defaultValue
        userEvent.clear(srcNamespace);
        userEvent.type(srcNamespace, inputs.srcNamespace);
    }
    if (inputs.src) userEvent.type(await screen.findByLabelText('Source'), inputs.src);
    if (inputs.srcPort) userEvent.type(await screen.findByLabelText('Source Port'), `${inputs.srcPort}`);
    if (inputs.destinationType) userEvent.click(await screen.findByLabelText(inputs.destinationType));
    if (inputs.dstNamespace) {
        const dstNamespace = await screen.findByLabelText('Destination Namespace');
        userEvent.clear(dstNamespace);
        userEvent.type(dstNamespace, inputs.dstNamespace);
    }
    if (inputs.dst) userEvent.type(await screen.findByLabelText('Destination'), inputs.dst);
    if (inputs.dstPort) {
        const dstPort = await screen.findByLabelText('Destination Port');
        userEvent.clear(dstPort);
        userEvent.type(dstPort, `${inputs.dstPort}`);
    }
    if (inputs.timeout) {
        const timeout = await screen.findByLabelText('Request Timeout');
        userEvent.clear(timeout);
        userEvent.type(timeout, `${inputs.timeout}`);
    }
}

describe('Traceflow form', () => {
    interface testCase {
        inputs: testInputs
        mustBePresent?: string[]
        mustNotBePresent?: string[]
    }

    const testCases: testCase[] = [
        {
            inputs: {
                liveTraffic: false,
                proto: 'TCP',
            },
            mustBePresent: ['Source Port', 'TCP Flags'],
            mustNotBePresent: ['Dropped Traffic Only'],
        },
        {
            inputs: {
                liveTraffic: false,
                proto: 'UDP',
            },
            mustBePresent: ['Source Port'],
            mustNotBePresent: ['TCP Flags', 'Dropped Traffic Only'],
        },
        {
            inputs: {
                liveTraffic: false,
                proto: 'ICMP',
            },
            mustNotBePresent: ['Source Port', 'TCP Flags', 'Dropped Traffic Only'],
        },
        {
            inputs: {
                liveTraffic: true,
                proto: 'TCP',
            },
            mustBePresent: ['Source Port', 'Dropped Traffic Only'],
            mustNotBePresent: ['TCP Flags'],
        },
        {
            inputs: {
                liveTraffic: true,
                proto: 'UDP',
            },
            mustBePresent: ['Source Port', 'Dropped Traffic Only'],
            mustNotBePresent: ['TCP Flags'],
        },
        {
            inputs: {
                liveTraffic: true,
                proto: 'ICMP',
            },
            mustBePresent: ['Dropped Traffic Only'],
            mustNotBePresent: ['Source Port', 'TCP Flags'],
        },
    ];

    test.each<testCase>(testCases)('check form fields - $inputs', async (tc: testCase) => {
        render(<Traceflow />, { wrapper: MemoryRouter });
        await inputsToEvents(tc.inputs);

        tc.mustBePresent?.forEach(x => expect(screen.getByLabelText(x)).toBeInTheDocument());
        tc.mustNotBePresent?.forEach(x => expect(screen.queryByLabelText(x)).toBeNull());
    });
});

describe('Traceflow request', () => {
    interface testCase {
        name: string
        inputs: testInputs
        expectedTf: TraceflowSpec
    }

    const testCases: testCase[] = [
        {
            name: 'regular',
            inputs: {
                liveTraffic: false,
                proto: 'TCP',
                ipv6: true,
                srcNamespace: 'namespaceA',
                src: 'podA',
                destinationType: 'Service',
                dstNamespace: 'namespaceA',
                dst: 'serviceA',
                dstPort: 80,
            },
            expectedTf: {
                source: {
                    namespace: 'namespaceA',
                    pod: 'podA',
                },
                destination: {
                    namespace: 'namespaceA',
                    service: 'serviceA',
                },
                packet: {
                    ipv6Header: {
                        nextHeader: 6,
                    },
                    transportHeader: {
                        tcp: {
                            srcPort: 0,
                            dstPort: 80,
                            flags: 2,
                        },
                    },
                },
                timeout: 20,
            } as TraceflowSpec,
        },
        {
            name: 'live',
            inputs: {
                liveTraffic: true,
                droppedOnly: true,
                proto: 'UDP',
                ipv6: false,
                destinationType: 'Pod',
                dstNamespace: 'namespaceA',
                dst: 'podA',
                timeout: 120,
            },
            expectedTf: {
                source: {},
                destination: {
                    namespace: 'namespaceA',
                    pod: 'podA',
                },
                packet: {
                    ipHeader: {
                        protocol: 17,
                    },
                    transportHeader: {
                        udp: {
                            srcPort: 0,
                            dstPort: 0,
                        },
                    },
                },
                liveTraffic: true,
                droppedOnly: true,
                timeout: 120,
            } as TraceflowSpec,
        },
    ];

    test.each<testCase>(testCases)('$name', async (tc: testCase) => {
        render(<Traceflow />, { wrapper: MemoryRouter });

        await inputsToEvents(tc.inputs);

        mockedTraceflowAPI.runTraceflow.mockResolvedValueOnce({} as TraceflowStatus);

        // unclear why this is needed, but without it the form is not submitted
        await userEvent.click(document.body);
        userEvent.click(screen.getByRole('button', {name: 'Run Traceflow'}));

        await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith('/traceflow/result', expect.anything()));

        await waitFor(() => expect(mockedTraceflowAPI.runTraceflow).toHaveBeenCalledWith(tc.expectedTf, true));
    });
});
