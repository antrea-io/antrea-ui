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

import { describe, it, expect } from 'vitest';
import { canViewFlows, observableNamespaces } from './flow-namespaces-api.js';
import type { FlowNamespacesResponse } from './flow-namespaces-api.js';

function resp(over: Partial<FlowNamespacesResponse> = {}): FlowNamespacesResponse {
    return { namespaces: [], clusterWide: false, incomplete: false, ...over };
}

describe('canViewFlows', () => {
    it('renders the page for a caller who may observe one namespace', () => {
        // The case the cluster-wide check it replaced got wrong, and the whole point of the
        // observed-namespace selector.
        expect(canViewFlows(resp({ namespaces: [{ namespace: 'flow-a', canObserve: true }] }))).toBe(true);
    });

    it('renders the page for a cluster-wide caller with no named namespace', () => {
        expect(canViewFlows(resp({ clusterWide: true }))).toBe(true);
    });

    it('hides the page when no namespace is observable', () => {
        expect(canViewFlows(resp({ namespaces: [{ namespace: 'flow-c', canObserve: false }] }))).toBe(false);
        expect(canViewFlows(resp())).toBe(false);
    });

    it('fails open before the response has loaded', () => {
        // Hiding a feature over a transient error is worse than showing one the Flow Aggregator
        // will refuse: authorization is its decision either way.
        expect(canViewFlows(null)).toBe(true);
    });

    it('does not consult incomplete, which can only mean under-reporting', () => {
        expect(canViewFlows(resp({ incomplete: true }))).toBe(false);
        expect(canViewFlows(resp({ incomplete: true, clusterWide: true }))).toBe(true);
    });
});

describe('observableNamespaces', () => {
    it('keeps only the observable ones, and tolerates null', () => {
        expect(observableNamespaces(resp({
            namespaces: [
                { namespace: 'flow-a', canObserve: true },
                { namespace: 'flow-c', canObserve: false },
            ],
        }))).toEqual(['flow-a']);
        expect(observableNamespaces(null)).toEqual([]);
    });
});
