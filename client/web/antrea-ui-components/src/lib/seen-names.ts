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

import { Flow, destinationK8sServiceFilterKey } from './flow-types.js';

// The same bound as the flow store's, per kind. A full store can show more Pods than that, two per
// connection: past the bound, the menus no longer offer the least recently seen ones.
const DEFAULT_MAX_PER_KIND = 10_000;

/** A Pod seen in flows, and the end(s) of a flow it was seen on. */
export interface SeenPod {
    namespace: string;
    name: string;
    source: boolean;
    destination: boolean;
}

/**
 * The names the flow filter menus offer: the Namespaces, Pods and Services seen in flows, kept
 * across the streams that replace each other while the filters change, so that a filter does not
 * take away the names the next one needs. Bounded per kind, the least recently seen dropped first.
 * Each kind is kept in the order last seen, oldest first.
 */
export class SeenNames {
    private nsMap = new Map<string, true>();
    private podMap = new Map<string, SeenPod>();
    private serviceMap = new Map<string, true>();
    private maxPerKind: number;

    constructor(maxPerKind: number = DEFAULT_MAX_PER_KIND) {
        this.maxPerKind = maxPerKind;
    }

    record(flow: Flow): void {
        const k8s = flow.k8s;
        if (k8s.sourcePodNamespace) this.touch(this.nsMap, k8s.sourcePodNamespace, true);
        const service = destinationK8sServiceFilterKey(k8s.destinationServicePortName);
        if (k8s.destinationPodNamespace) this.touch(this.nsMap, k8s.destinationPodNamespace, true);
        // The Flow Aggregator places a destination without a Pod namespace, such as the backend
        // of default/kubernetes, in its Service's namespace.
        else if (service) this.touch(this.nsMap, service.split('/')[0], true);
        // A Pod without a name was not identified to this user: there is nothing to offer.
        if (k8s.sourcePodName) this.touchPod(k8s.sourcePodNamespace, k8s.sourcePodName, 'source');
        if (k8s.destinationPodName) this.touchPod(k8s.destinationPodNamespace, k8s.destinationPodName, 'destination');
        if (service) this.touch(this.serviceMap, service, true);
    }

    recordBatch(flows: Flow[]): void {
        for (const flow of flows) this.record(flow);
    }

    namespaces(): string[] {
        return Array.from(this.nsMap.keys());
    }

    pods(): SeenPod[] {
        return Array.from(this.podMap.values());
    }

    /** As namespace/name. */
    services(): string[] {
        return Array.from(this.serviceMap.keys());
    }

    reset(): void {
        this.nsMap.clear();
        this.podMap.clear();
        this.serviceMap.clear();
    }

    // Moves the key to the end, as the most recently seen, and drops the least recently seen past
    // the bound.
    private touch<V>(map: Map<string, V>, key: string, value: V): void {
        map.delete(key);
        map.set(key, value);
        if (map.size > this.maxPerKind) map.delete(map.keys().next().value!);
    }

    private touchPod(namespace: string, name: string, end: 'source' | 'destination'): void {
        const key = `${namespace}/${name}`;
        const pod = this.podMap.get(key) ?? { namespace, name, source: false, destination: false };
        pod[end] = true;
        this.touch(this.podMap, key, pod);
    }
}
