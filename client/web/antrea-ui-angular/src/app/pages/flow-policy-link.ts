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

import { Component, CUSTOM_ELEMENTS_SCHEMA, input } from '@angular/core';
import { RouterLink } from '@angular/router';
import type { EdgeSelection } from '@antrea/ui-components/dist';

// Slotted into <antrea-flow-visibility-page>'s "edge-extra" slot (see
// pages/flow-visibility-page.ts). This is the downstream half of the
// antrea-edge-selected extension point: the upstream Lit component only knows
// how to report what's selected and whether it's policy-protected; it has no
// idea a "Policy Management" page even exists. All of the policy-management
// specific behavior — which page to link to, and what to pass it — lives here.
@Component({
  selector: 'app-flow-policy-link',
  standalone: true,
  imports: [RouterLink],
  template: `
    @if (selection(); as s) {
      <div class="policy-link-row">
        @if (s.protected) {
          <a routerLink="/policies/definitions" [queryParams]="{ policy: firstPolicyName(s) }">
            <cds-icon shape="shield-check"></cds-icon>
            View policy definition
          </a>
        } @else {
          <a routerLink="/policies/recommendations" [queryParams]="{ source: s.source, target: s.target, ports: s.destPorts }">
            <cds-icon shape="bolt"></cds-icon>
            Get policy recommendation
          </a>
        }
      </div>
    }
  `,
  styles: [`
    .policy-link-row {
      margin-top: 12px;
      padding-top: 12px;
      border-top: 1px solid var(--antrea-color-border, #314351);
    }
    a {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      font-size: 0.8125rem;
      color: var(--antrea-color-primary, #0072a3);
      text-decoration: none;
    }
    a:hover {
      text-decoration: underline;
    }
  `],
  schemas: [CUSTOM_ELEMENTS_SCHEMA],
})
export class FlowPolicyLink {
  readonly selection = input<EdgeSelection | null>(null);

  protected firstPolicyName(s: EdgeSelection): string {
    return s.ingressPolicyNames[0] ?? s.egressPolicyNames[0] ?? '';
  }
}
