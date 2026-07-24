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

import { Component, CUSTOM_ELEMENTS_SCHEMA, Injector, inject, signal } from '@angular/core';
import { toSignal } from '@angular/core/rxjs-interop';
import { ActivatedRoute } from '@angular/router';
import { map } from 'rxjs';
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
      [viewMode]="viewMode()"
      hide-view-toggle
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
  private readonly route = inject(ActivatedRoute);
  private readonly injector = inject(Injector);

  // Flow List / Service Map is now switched from the left nav (see nav.html) instead of the
  // component's own built-in tab strip (hidden above via hide-view-toggle), so the two nav
  // entries route to the same page with a ?view= query param rather than remounting it —
  // that keeps the live SSE connection and applied filters intact across the switch.
  //
  // Explicit injector: toSignal() otherwise asserts it's being called synchronously in an
  // injection context, which this field initializer technically is — but the Router's own
  // component-activation path (Object.factory -> activateWith) doesn't leave that assertion
  // satisfied here, so pass it directly rather than relying on the implicit lookup.
  protected readonly viewMode = toSignal(
    this.route.queryParamMap.pipe(map(params => (params.get('view') === 'map' ? 'map' as const : 'list' as const))),
    { initialValue: 'list' as const, injector: this.injector },
  );

  protected readonly selection = signal<EdgeSelection | null>(null);

  protected onEdgeSelected(e: Event): void {
    this.selection.set((e as CustomEvent<EdgeSelection | null>).detail);
  }
}
