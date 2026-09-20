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

import { describe, expect, test } from 'vitest';
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
    destinationK8sServiceFilterKey,
    EndpointDisclosure,
    endpointRedacted,
    endpointView,
    destinationServiceView,
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

describe('destinationK8sServiceFilterKey', () => {
    test('strips named port from namespace/service', () => {
        expect(destinationK8sServiceFilterKey('flow-demo-a/agnhost-server:http')).toBe(
            'flow-demo-a/agnhost-server',
        );
    });

    test('strips grpc port suffix', () => {
        expect(destinationK8sServiceFilterKey('flow-aggregator/flow-aggregator:grpc')).toBe(
            'flow-aggregator/flow-aggregator',
        );
    });

    test('leaves namespace/service without port unchanged', () => {
        expect(destinationK8sServiceFilterKey('default/frontend')).toBe('default/frontend');
    });

    test('no namespace returns empty string', () => {
        expect(destinationK8sServiceFilterKey('agnhost-server:http')).toBe('');
    });

    test('empty namespace returns empty string', () => {
        expect(destinationK8sServiceFilterKey('/agnhost-server:http')).toBe('');
    });

    test('service name only returns empty string', () => {
        expect(destinationK8sServiceFilterKey('frontend')).toBe('');
    });

    test('empty input', () => {
        expect(destinationK8sServiceFilterKey('')).toBe('');
    });
});

// Keyed on the action, not the name. At the Flow tier the policy name is cleared and the rule
// action is not, so a formatter that rendered nothing without a name would show a dropped flow to
// an undisclosed peer as a bare '-' - indistinguishable from a flow no policy ever matched, and
// throwing away the field redaction deliberately preserved.
describe('formatPolicyInfo', () => {
    test('an empty name at the Flow tier renders the action as withheld', () => {
        const flow = EndpointDisclosure.Flow;
        expect(formatPolicyInfo('', NetworkPolicyRuleAction.Drop, flow)).toBe('Dropped (policy hidden)');
        expect(formatPolicyInfo('', NetworkPolicyRuleAction.Reject, flow)).toBe('Rejected (policy hidden)');
        // One tense across the three, so they read as one set rather than three phrasings.
        expect(formatPolicyInfo('', NetworkPolicyRuleAction.Allow, flow)).toBe('Allowed (policy hidden)');
    });

    // Above the Flow tier nothing was withheld, so an empty name means the Flow Aggregator never
    // had one - its proto warns an endpoint can lack a field while still reporting full
    // disclosure. Calling that "hidden" would blame redaction for a gap it did not cause.
    test('an empty name above the Flow tier renders the action without claiming redaction', () => {
        expect(formatPolicyInfo('', NetworkPolicyRuleAction.Drop, EndpointDisclosure.Full)).toBe('Dropped');
        expect(formatPolicyInfo('', NetworkPolicyRuleAction.Drop, EndpointDisclosure.Identity)).toBe('Dropped');
    });

    // A caller that cannot say assumes withheld, which is the conservative reading.
    test('an omitted tier assumes the name was withheld', () => {
        expect(formatPolicyInfo('', NetworkPolicyRuleAction.Drop)).toBe('Dropped (policy hidden)');
    });

    // The genuinely absent policy, which is not a redaction artefact and must not be marked as
    // one: a policy that matched always carries an action, so this was already empty before
    // redaction ran.
    test('an empty name with no action renders nothing, at any tier', () => {
        expect(formatPolicyInfo('', NetworkPolicyRuleAction.NoAction)).toBe('');
        expect(formatPolicyInfo('', NetworkPolicyRuleAction.NoAction, EndpointDisclosure.Flow)).toBe('');
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

describe('endpointRedacted', () => {
    // Full is the enum's zero value, and the entire Full-over-Identity delta is fields this UI
    // does not render, so Identity is deliberately indistinguishable from Full here.
    test('only the Flow tier counts as withheld', () => {
        expect(endpointRedacted(EndpointDisclosure.Full)).toBe(false);
        expect(endpointRedacted(EndpointDisclosure.Identity)).toBe(false);
        expect(endpointRedacted(EndpointDisclosure.Flow)).toBe(true);
    });

    test('a missing marker reads as Full, for a record from an older backend', () => {
        expect(endpointRedacted(undefined)).toBe(false);
    });
});

describe('endpointView', () => {
    test('an identified endpoint is named, unmarked, and shows its address', () => {
        expect(endpointView(EndpointDisclosure.Identity, 'ns-b', 'db-abc12', '10.0.0.5'))
            .toEqual({ text: 'ns-b/db-abc12', detail: '10.0.0.5', redacted: false });
    });

    // The Flow tier on an allowed connection: the namespace survives, the workload does not, and
    // the tooltip can name the grant that would disclose it.
    test('a withheld workload keeps its namespace and gets an actionable tooltip', () => {
        const view = endpointView(EndpointDisclosure.Flow, 'ns-c', '', '10.0.0.7');
        expect(view.text).toBe('ns-c/\u27e8hidden\u27e9');
        expect(view.redacted).toBe(true);
        expect(view.tooltip).toContain('get flows/identity on ns-c');
    });

    // The denied-connection case: redactFlow clears the namespace too, to close a Pod-CIDR
    // enumeration oracle. There is no namespace to name, so there is nothing actionable to say.
    test('a withheld endpoint with no namespace falls back to the address, with no tooltip', () => {
        const view = endpointView(EndpointDisclosure.Flow, '', '', '10.0.0.9');
        expect(view).toEqual({ text: '10.0.0.9', redacted: true });
    });
});

describe('destinationServiceView', () => {
    test('a disclosed service renders its name, unmarked', () => {
        expect(destinationServiceView(EndpointDisclosure.Full, 'ns-b/frontend:http'))
            .toEqual({ text: 'ns-b/frontend', redacted: false });
    });

    // Its own string rather than the peer's: with a lock already in the Destination cell, a
    // second unexplained one reads as a duplicate rather than as a separate withheld field.
    test('a withheld service is marked and explained', () => {
        const view = destinationServiceView(EndpointDisclosure.Flow, '');
        expect(view.redacted).toBe(true);
        expect(view.tooltip).toContain("destination's namespace");
    });

    test('an identified destination with no service is simply empty', () => {
        expect(destinationServiceView(EndpointDisclosure.Full, ''))
            .toEqual({ text: '-', redacted: false });
    });
});

describe('external endpoints are not marked as redacted', () => {
    // An out-of-cluster endpoint reaches the Flow tier because tierFor resolves an endpoint with
    // no namespace that way, but nothing was withheld from it. Upstream's documented way to tell
    // the two apart is the flow type, which is never redacted.
    it('renders an external endpoint plainly at the Flow tier', () => {
        const v = endpointView(EndpointDisclosure.Flow, '', '', '10.89.0.2', true);
        expect(v.redacted).toBe(false);
        expect(v.text).toBe('10.89.0.2');
        expect(v.tooltip).toBeUndefined();
    });

    it('still marks an in-cluster endpoint at the Flow tier', () => {
        const v = endpointView(EndpointDisclosure.Flow, '', '', '10.1.9.21', false);
        expect(v.redacted).toBe(true);
    });

    it('leaves an external destination service genuinely empty', () => {
        const v = destinationServiceView(EndpointDisclosure.Flow, '', true);
        expect(v.redacted).toBe(false);
        expect(v.text).toBe('-');
    });

    it('still marks a withheld in-cluster destination service', () => {
        const v = destinationServiceView(EndpointDisclosure.Flow, '', false);
        expect(v.redacted).toBe(true);
    });
});

describe('a withheld workload still shows its address', () => {
    // Without it the cell reads "ns/<hidden>" and identifies nothing: two peers in the same
    // namespace become indistinguishable, which is most of what the row was for.
    it('carries the address as detail when the namespace survived', () => {
        const v = endpointView(EndpointDisclosure.Flow, 'flow-c', '', '10.244.1.14');
        expect(v.text).toBe('flow-c/⟨hidden⟩');
        expect(v.detail).toBe('10.244.1.14');
        expect(v.redacted).toBe(true);
    });

    it('needs no detail when the address is already the text', () => {
        // The denied case: the namespace went too, so the address is the label itself.
        const v = endpointView(EndpointDisclosure.Flow, '', '', '10.244.1.20');
        expect(v.text).toBe('10.244.1.20');
        expect(v.detail).toBeUndefined();
    });

    it('shows it under an unredacted endpoint too, so rows keep one shape', () => {
        const v = endpointView(EndpointDisclosure.Identity, 'flow-b', 'server-abc', '10.244.2.5');
        expect(v.text).toBe('flow-b/server-abc');
        expect(v.detail).toBe('10.244.2.5');
        expect(v.redacted).toBe(false);
    });

    it('does not repeat an address that is already the label', () => {
        // formatEndpoint falls back to the address when there is no name to show.
        expect(endpointView(EndpointDisclosure.Full, '', '', '10.244.3.9').detail).toBeUndefined();
        expect(endpointView(EndpointDisclosure.Flow, '', '', '10.89.0.2', true).detail).toBeUndefined();
    });
});
