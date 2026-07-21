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

import { Component, CUSTOM_ELEMENTS_SCHEMA, signal } from '@angular/core';
import type { EdgeSelection } from '@antrea/ui-components/dist';
import { TokenBoundPage } from '../core/token-bound-page';
import { FlowPolicyLink } from './flow-policy-link';

@Component({
  selector: 'app-flow-visibility-page',
  standalone: true,
  imports: [FlowPolicyLink],
  template: `
    <antrea-flow-visibility-page
      [token]="auth.token()"
      (antrea-session-expired)="auth.sessionExpired()"
      (antrea-edge-selected)="onEdgeSelected($event)">
      <div slot="edge-extra">
        <app-flow-policy-link [selection]="selection()"></app-flow-policy-link>
      </div>
    </antrea-flow-visibility-page>
  `,
  schemas: [CUSTOM_ELEMENTS_SCHEMA],
})
export class FlowVisibilityPage extends TokenBoundPage {
  protected readonly selection = signal<EdgeSelection | null>(null);

  protected onEdgeSelected(e: Event): void {
    this.selection.set((e as CustomEvent<EdgeSelection | null>).detail);
  }
}
