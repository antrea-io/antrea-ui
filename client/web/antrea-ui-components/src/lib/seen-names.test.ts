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
import { SeenNames } from './seen-names';
import {
    Flow,
    FlowType,
    FlowEndReason,
    IPVersion,
    NetworkPolicyType,
    NetworkPolicyRuleAction,
} from './flow-types';

interface Ends {
    srcNs?: string;
    srcPod?: string;
    dstNs?: string;
    dstPod?: string;
    service?: string;
}

function makeFlow(ends: Ends): Flow {
    return {
        id: '',
        startTs: '2026-03-25T00:00:00Z',
        endTs: '2026-03-25T00:01:00Z',
        endReason: FlowEndReason.Unspecified,
        ip: { version: IPVersion.IPv4, source: '10.0.0.1', destination: '10.0.0.2' },
        transport: { protocolNumber: 6, sourcePort: 12345, destinationPort: 80 },
        k8s: {
            flowType: FlowType.InterNode,
            sourcePodNamespace: ends.srcNs ?? '',
            sourcePodName: ends.srcPod ?? '',
            sourcePodUid: '',
            sourceNodeName: '',
            sourceNodeUid: '',
            destinationPodNamespace: ends.dstNs ?? '',
            destinationPodName: ends.dstPod ?? '',
            destinationPodUid: '',
            destinationNodeName: '',
            destinationNodeUid: '',
            destinationClusterIp: '',
            destinationServicePort: 0,
            destinationServicePortName: ends.service ?? '',
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

describe('SeenNames', () => {
    test('records the namespaces, Pods and Service of a flow', () => {
        const names = new SeenNames();
        names.record(makeFlow({ srcNs: 'ns-a', srcPod: 'client', dstNs: 'ns-b', dstPod: 'web', service: 'ns-b/web:http' }));
        expect(names.namespaces()).toEqual(['ns-a', 'ns-b']);
        expect(names.pods()).toEqual([
            { namespace: 'ns-a', name: 'client', source: true, destination: false },
            { namespace: 'ns-b', name: 'web', source: false, destination: true },
        ]);
        expect(names.services()).toEqual(['ns-b/web']);
    });

    test('a Pod seen on both ends is recorded once, with both', () => {
        const names = new SeenNames();
        names.record(makeFlow({ srcNs: 'ns-a', srcPod: 'web', dstNs: 'ns-a', dstPod: 'db' }));
        names.record(makeFlow({ srcNs: 'ns-a', srcPod: 'client', dstNs: 'ns-a', dstPod: 'web' }));
        expect(names.pods().filter(p => p.name === 'web')).toEqual([
            { namespace: 'ns-a', name: 'web', source: true, destination: true },
        ]);
    });

    test('Pods of the same name in two namespaces are two Pods', () => {
        const names = new SeenNames();
        names.record(makeFlow({ srcNs: 'ns-a', srcPod: 'web', dstNs: 'ns-b', dstPod: 'web' }));
        expect(names.pods().map(p => p.namespace)).toEqual(['ns-a', 'ns-b']);
    });

    // The Flow Aggregator places such a destination, as the backend of default/kubernetes, in its
    // Service's namespace.
    test('a destination without a Pod namespace adds its Service and the Service namespace', () => {
        const names = new SeenNames();
        names.record(makeFlow({ srcNs: 'ns-a', srcPod: 'client', service: 'default/kubernetes:https' }));
        expect(names.namespaces()).toEqual(['ns-a', 'default']);
        expect(names.pods().map(p => p.name)).toEqual(['client']);
        expect(names.services()).toEqual(['default/kubernetes']);
    });

    // The Service's namespace need not be its endpoint's, which is where the destination is.
    test('a destination with a Pod namespace does not add its Service namespace', () => {
        const names = new SeenNames();
        names.record(makeFlow({ srcNs: 'ns-a', srcPod: 'client', dstNs: 'ns-c', dstPod: 'web', service: 'ns-b/xsvc:http' }));
        expect(names.namespaces()).toEqual(['ns-a', 'ns-c']);
        expect(names.services()).toEqual(['ns-b/xsvc']);
    });

    // The Flow tier clears the Pod name and keeps the namespace.
    test('an endpoint not identified adds its namespace and no Pod', () => {
        const names = new SeenNames();
        names.record(makeFlow({ srcNs: 'ns-a', dstNs: 'ns-b', dstPod: 'web' }));
        expect(names.namespaces()).toEqual(['ns-a', 'ns-b']);
        expect(names.pods().map(p => p.name)).toEqual(['web']);
    });

    test('a withheld endpoint adds nothing', () => {
        const names = new SeenNames();
        names.record(makeFlow({}));
        expect([names.namespaces(), names.pods(), names.services()]).toEqual([[], [], []]);
    });

    test('drops the least recently seen name of a kind past the bound', () => {
        const names = new SeenNames(2);
        names.record(makeFlow({ srcNs: 'ns-a', srcPod: 'a', service: 'ns-a/x:http' }));
        names.record(makeFlow({ srcNs: 'ns-b', srcPod: 'b', service: 'ns-b/y:http' }));
        // Seen again, so no longer the least recently seen.
        names.record(makeFlow({ srcNs: 'ns-a', srcPod: 'a', service: 'ns-a/x:http' }));
        names.record(makeFlow({ srcNs: 'ns-c', srcPod: 'c', service: 'ns-c/z:http' }));
        expect(names.namespaces()).toEqual(['ns-a', 'ns-c']);
        expect(names.pods().map(p => p.name)).toEqual(['a', 'c']);
        expect(names.services()).toEqual(['ns-a/x', 'ns-c/z']);
    });

    test('reset forgets every name', () => {
        const names = new SeenNames();
        names.record(makeFlow({ srcNs: 'ns-a', srcPod: 'client', dstNs: 'ns-b', dstPod: 'web', service: 'ns-b/web:http' }));
        names.reset();
        expect([names.namespaces(), names.pods(), names.services()]).toEqual([[], [], []]);
    });
});
