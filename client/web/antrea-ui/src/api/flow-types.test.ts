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

import {
    Flow,
    FlowType,
    FlowEndReason,
    IPVersion,
    NetworkPolicyType,
    NetworkPolicyRuleAction,
    connectionKey,
    formatBytes,
    getProtocolName,
    formatEndpoint,
    formatPolicyInfo,
} from './flow-types';

function makeFlow(overrides: {
    srcIP?: string;
    dstIP?: string;
    srcPort?: number;
    dstPort?: number;
    proto?: number;
}): Flow {
    return {
        id: 'test-flow',
        startTs: '2026-03-25T00:00:00Z',
        endTs: '2026-03-25T00:01:00Z',
        endReason: FlowEndReason.Unspecified,
        ip: {
            version: IPVersion.IPv4,
            source: overrides.srcIP ?? '10.0.0.1',
            destination: overrides.dstIP ?? '10.0.0.2',
        },
        transport: {
            protocolNumber: overrides.proto ?? 6,
            sourcePort: overrides.srcPort ?? 12345,
            destinationPort: overrides.dstPort ?? 80,
        },
        k8s: {
            flowType: FlowType.InterNode,
            sourcePodNamespace: 'default',
            sourcePodName: 'pod-a',
            sourcePodUid: '',
            sourceNodeName: 'node-1',
            sourceNodeUid: '',
            destinationPodNamespace: 'default',
            destinationPodName: 'pod-b',
            destinationPodUid: '',
            destinationNodeName: 'node-2',
            destinationNodeUid: '',
            destinationClusterIp: '',
            destinationServicePort: 0,
            destinationServicePortName: '',
            destinationServiceUid: '',
            ingressNetworkPolicyType: NetworkPolicyType.Unspecified,
            ingressNetworkPolicyNamespace: '',
            ingressNetworkPolicyName: '',
            ingressNetworkPolicyUid: '',
            ingressNetworkPolicyRuleName: '',
            ingressNetworkPolicyRuleAction: NetworkPolicyRuleAction.NoAction,
            egressNetworkPolicyType: NetworkPolicyType.Unspecified,
            egressNetworkPolicyNamespace: '',
            egressNetworkPolicyName: '',
            egressNetworkPolicyUid: '',
            egressNetworkPolicyRuleName: '',
            egressNetworkPolicyRuleAction: NetworkPolicyRuleAction.NoAction,
            egressName: '',
            egressIp: '',
            egressNodeName: '',
            egressNodeUid: '',
            egressUid: '',
        },
        stats: { packetTotalCount: 0, packetDeltaCount: 0, octetTotalCount: 0, octetDeltaCount: 0 },
        reverseStats: { packetTotalCount: 0, packetDeltaCount: 0, octetTotalCount: 0, octetDeltaCount: 0 },
    };
}

describe('connectionKey', () => {
    test('masks source port', () => {
        const flow1 = makeFlow({ srcPort: 11111 });
        const flow2 = makeFlow({ srcPort: 22222 });
        expect(connectionKey(flow1)).toBe(connectionKey(flow2));
    });

    test('different destination port produces different key', () => {
        const flow1 = makeFlow({ dstPort: 80 });
        const flow2 = makeFlow({ dstPort: 443 });
        expect(connectionKey(flow1)).not.toBe(connectionKey(flow2));
    });

    test('different source IP produces different key', () => {
        const flow1 = makeFlow({ srcIP: '10.0.0.1' });
        const flow2 = makeFlow({ srcIP: '10.0.0.99' });
        expect(connectionKey(flow1)).not.toBe(connectionKey(flow2));
    });

    test('different destination IP produces different key', () => {
        const flow1 = makeFlow({ dstIP: '10.0.0.2' });
        const flow2 = makeFlow({ dstIP: '10.0.0.99' });
        expect(connectionKey(flow1)).not.toBe(connectionKey(flow2));
    });

    test('different protocol produces different key', () => {
        const flow1 = makeFlow({ proto: 6 });
        const flow2 = makeFlow({ proto: 17 });
        expect(connectionKey(flow1)).not.toBe(connectionKey(flow2));
    });

    test('key format is srcIP|dstIP|proto|dstPort', () => {
        const flow = makeFlow({ srcIP: '1.2.3.4', dstIP: '5.6.7.8', proto: 6, dstPort: 443 });
        expect(connectionKey(flow)).toBe('1.2.3.4|5.6.7.8|6|443');
    });
});

describe('formatBytes', () => {
    test('zero bytes', () => {
        expect(formatBytes(0)).toBe('0 B');
    });

    test('bytes', () => {
        expect(formatBytes(500)).toBe('500 B');
    });

    test('kilobytes', () => {
        expect(formatBytes(1024)).toBe('1.0 KB');
    });

    test('megabytes', () => {
        expect(formatBytes(1048576)).toBe('1.0 MB');
    });

    test('gigabytes', () => {
        expect(formatBytes(1073741824)).toBe('1.0 GB');
    });

    test('fractional kilobytes', () => {
        expect(formatBytes(1536)).toBe('1.5 KB');
    });
});

describe('getProtocolName', () => {
    test('TCP', () => {
        expect(getProtocolName(6)).toBe('TCP');
    });

    test('UDP', () => {
        expect(getProtocolName(17)).toBe('UDP');
    });

    test('ICMP', () => {
        expect(getProtocolName(1)).toBe('ICMP');
    });

    test('ICMPv6', () => {
        expect(getProtocolName(58)).toBe('ICMPv6');
    });

    test('SCTP', () => {
        expect(getProtocolName(132)).toBe('SCTP');
    });

    test('unknown protocol', () => {
        expect(getProtocolName(99)).toBe('Proto(99)');
    });
});

describe('formatEndpoint', () => {
    test('namespace and pod name', () => {
        expect(formatEndpoint('default', 'my-pod', '10.0.0.1')).toBe('default/my-pod');
    });

    test('IP fallback when no namespace', () => {
        expect(formatEndpoint('', '', '10.0.0.1')).toBe('10.0.0.1');
    });

    test('IP fallback when no pod name', () => {
        expect(formatEndpoint('default', '', '10.0.0.1')).toBe('10.0.0.1');
    });

    test('unknown when nothing provided', () => {
        expect(formatEndpoint('', '', '')).toBe('unknown');
    });
});

describe('formatPolicyInfo', () => {
    test('empty name returns empty string', () => {
        expect(formatPolicyInfo('', NetworkPolicyRuleAction.Allow)).toBe('');
    });

    test('name with Allow action', () => {
        expect(formatPolicyInfo('my-policy', NetworkPolicyRuleAction.Allow)).toBe('my-policy (Allow)');
    });

    test('name with Drop action', () => {
        expect(formatPolicyInfo('my-policy', NetworkPolicyRuleAction.Drop)).toBe('my-policy (Drop)');
    });

    test('name with Reject action', () => {
        expect(formatPolicyInfo('my-policy', NetworkPolicyRuleAction.Reject)).toBe('my-policy (Reject)');
    });

    test('name with NoAction returns name only', () => {
        expect(formatPolicyInfo('my-policy', NetworkPolicyRuleAction.NoAction)).toBe('my-policy');
    });
});
