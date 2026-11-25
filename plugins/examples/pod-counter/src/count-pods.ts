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

import type { NodeCardItem } from '@antrea/ui-plugin-sdk';

export async function countPods(namespace?: string, signal?: AbortSignal): Promise<number> {
    const path = namespace ? `namespaces/${encodeURIComponent(namespace)}/pods` : 'pods';
    // Kubernetes normally supplies the remaining count on a limited, unfiltered list.
    // Follow the continuation only if the server omitted it.
    let count = 0;
    const params = new URLSearchParams({ limit: '100' });
    for (;;) {
        const res = await fetch(`/api/v1/k8s/api/v1/${path}?${params}`, { credentials: 'include', signal });
        if (res.status === 403) throw new Error('you do not have permission to list Pods');
        if (!res.ok) throw new Error(`request failed: ${res.status}`);
        const list = await res.json();
        count += list.items.length;
        if (!list.metadata?.continue) return count;
        if (typeof list.metadata.remainingItemCount === 'number') return count + list.metadata.remainingItemCount;
        params.set('continue', list.metadata.continue);
    }
}

export async function namespacePodItems(namespace: string, signal: AbortSignal, clusterPage?: string): Promise<NodeCardItem[]> {
    const label = `Pods in ${namespace}`;
    let count: number;
    try {
        count = await countPods(namespace, signal);
    } catch (error) {
        if (signal.aborted) return [];
        return [{ kind: 'value', label, value: error instanceof Error ? error.message : 'Failed to load the Pod count' }];
    }
    const items: NodeCardItem[] = [{ kind: 'value', label, value: String(count) }];
    if (clusterPage) items.push({ kind: 'link', label: 'Open cluster Pod Counter', href: clusterPage });
    return items;
}
