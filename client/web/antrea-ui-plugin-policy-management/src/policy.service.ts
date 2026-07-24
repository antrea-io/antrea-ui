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

import { Injectable, inject } from '@angular/core';
import { ANTREA_PLUGIN_HOST } from '@antrea/ui-plugin-sdk';

export type PolicyType = 'K8s NetworkPolicy' | 'Antrea NetworkPolicy' | 'Antrea ClusterNetworkPolicy';

export interface PolicyDefinition {
  key: string;
  name: string;
  namespace?: string;
  type: PolicyType;
  podSelector: string;
  createdAt?: string;
}

interface K8sObjectMeta {
  name: string;
  namespace?: string;
  creationTimestamp?: string;
}

interface K8sNetworkPolicyList {
  items: {
    metadata: K8sObjectMeta;
    spec?: { podSelector?: { matchLabels?: Record<string, string> } };
  }[];
}

interface AntreaClusterNetworkPolicyList {
  items: {
    metadata: K8sObjectMeta;
    spec?: { appliedTo?: { podSelector?: { matchLabels?: Record<string, string> } }[] };
  }[];
}

export interface PolicyListResult {
  policies: PolicyDefinition[];
  // Per-source fetch failures (e.g. missing RBAC permissions). Reported
  // separately from "no policies" so a 403 doesn't silently look like an
  // empty cluster.
  errors: string[];
}

// This service demonstrates that a downstream page can read live cluster state
// through the same backend K8s API proxy used by antrea-summary-page
// (/api/v1/k8s/<apiserver path>), rather than requiring a bespoke backend
// endpoint per downstream feature.
@Injectable({ providedIn: 'root' })
export class PolicyService {
  private readonly host = inject(ANTREA_PLUGIN_HOST);

  async listAll(): Promise<PolicyListResult> {
    const [k8sNp, antreaNp, acnp] = await Promise.allSettled([
      this.fetch<K8sNetworkPolicyList>('apis/networking.k8s.io/v1/networkpolicies'),
      this.fetch<K8sNetworkPolicyList>('apis/crd.antrea.io/v1beta1/networkpolicies'),
      this.fetch<AntreaClusterNetworkPolicyList>('apis/crd.antrea.io/v1beta1/clusternetworkpolicies'),
    ]);

    const result: PolicyDefinition[] = [];
    const errors: string[] = [];
    const reasonMessage = (reason: unknown) => reason instanceof Error ? reason.message : String(reason);

    if (k8sNp.status === 'rejected') errors.push(`K8s NetworkPolicy: ${reasonMessage(k8sNp.reason)}`);
    if (antreaNp.status === 'rejected') errors.push(`Antrea NetworkPolicy: ${reasonMessage(antreaNp.reason)}`);
    if (acnp.status === 'rejected') errors.push(`Antrea ClusterNetworkPolicy: ${reasonMessage(acnp.reason)}`);

    if (k8sNp.status === 'fulfilled') {
      for (const item of k8sNp.value.items) {
        result.push({
          key: `k8s/${item.metadata.namespace}/${item.metadata.name}`,
          name: item.metadata.name,
          namespace: item.metadata.namespace,
          type: 'K8s NetworkPolicy',
          podSelector: formatSelector(item.spec?.podSelector?.matchLabels),
          createdAt: item.metadata.creationTimestamp,
        });
      }
    }
    if (antreaNp.status === 'fulfilled') {
      for (const item of antreaNp.value.items) {
        result.push({
          key: `antrea-np/${item.metadata.namespace}/${item.metadata.name}`,
          name: item.metadata.name,
          namespace: item.metadata.namespace,
          type: 'Antrea NetworkPolicy',
          podSelector: formatSelector(item.spec?.podSelector?.matchLabels),
          createdAt: item.metadata.creationTimestamp,
        });
      }
    }
    if (acnp.status === 'fulfilled') {
      for (const item of acnp.value.items) {
        const selector = item.spec?.appliedTo?.[0]?.podSelector?.matchLabels;
        result.push({
          key: `acnp/${item.metadata.name}`,
          name: item.metadata.name,
          type: 'Antrea ClusterNetworkPolicy',
          podSelector: formatSelector(selector),
          createdAt: item.metadata.creationTimestamp,
        });
      }
    }

    return { policies: result, errors };
  }

  private async fetch<T>(path: string): Promise<T> {
    const token = this.host.getAuthToken() ?? '';
    const res = await fetch(`/api/v1/k8s/${path}`, {
      headers: token ? { Authorization: `Bearer ${token}` } : {},
    });
    if (!res.ok) {
      throw new Error(`HTTP ${res.status} fetching ${path}`);
    }
    return res.json();
  }
}

function formatSelector(labels: Record<string, string> | undefined): string {
  if (!labels || Object.keys(labels).length === 0) return '(all pods)';
  return Object.entries(labels).map(([k, v]) => `${k}=${v}`).join(', ');
}
