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

import { Component, computed, inject } from '@angular/core';
import { ActivatedRoute } from '@angular/router';
import { toSignal } from '@angular/core/rxjs-interop';

function shortName(workload: string): string {
  const parts = workload.split('/');
  return parts[parts.length - 1];
}

function namespaceOf(workload: string): string {
  const parts = workload.split('/');
  return parts.length > 1 ? parts[0] : 'default';
}

// NOTE: this is a static template, not a real recommendation engine — it only
// demonstrates that the flow's actual endpoints and ports (passed in via query
// params from the flow card) can drive a genuinely useful starting point.
// A real implementation would replace this with a call to a recommendation
// backend/CRD (e.g. Antrea's PolicyRecommendation, if enabled).
function buildRecommendation(source: string, target: string, ports: string): string {
  const srcName = shortName(source);
  const dstName = shortName(target);
  const dstNs = namespaceOf(target);
  const portsComment = ports ? `\n  # observed ports: ${ports}` : '';
  return `apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-${srcName}-to-${dstName}
  namespace: ${dstNs}
spec:
  podSelector:
    matchLabels:
      app: ${dstName}
  policyTypes:
    - Ingress
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app: ${srcName}${portsComment}
`;
}

@Component({
  selector: 'app-policy-recommendations-page',
  standalone: true,
  templateUrl: './policy-recommendations-page.html',
  styleUrl: './policy-management-page.css',
})
export class PolicyRecommendationsPage {
  private readonly route = inject(ActivatedRoute);

  private readonly queryParams = toSignal(this.route.queryParamMap);
  protected readonly source = computed(() => this.queryParams()?.get('source') ?? '');
  protected readonly target = computed(() => this.queryParams()?.get('target') ?? '');
  protected readonly ports = computed(() => this.queryParams()?.get('ports') ?? '');

  protected readonly recommendedYaml = computed(() => {
    if (!this.source() || !this.target()) return '';
    return buildRecommendation(this.source(), this.target(), this.ports());
  });
}
