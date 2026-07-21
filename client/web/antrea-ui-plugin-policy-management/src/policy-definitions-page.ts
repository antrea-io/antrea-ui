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

import { Component, computed, inject, signal } from '@angular/core';
import { ActivatedRoute } from '@angular/router';
import { toSignal } from '@angular/core/rxjs-interop';
import { PolicyDefinition, PolicyService } from './policy.service.js';

@Component({
  selector: 'app-policy-definitions-page',
  standalone: true,
  templateUrl: './policy-definitions-page.html',
  styleUrl: './policy-management-page.css',
})
export class PolicyDefinitionsPage {
  private readonly route = inject(ActivatedRoute);
  private readonly policyService = inject(PolicyService);

  protected readonly policies = signal<PolicyDefinition[]>([]);
  protected readonly loading = signal(true);
  // Fatal error (listAll() itself threw, e.g. no network at all).
  protected readonly error = signal('');
  // Per-source failures (e.g. missing RBAC permissions on one resource type) —
  // reported separately from "no policies" so a 403 doesn't look like an
  // empty cluster.
  protected readonly sourceErrors = signal<string[]>([]);

  private readonly queryParams = toSignal(this.route.queryParamMap);
  protected readonly highlightName = computed(() => this.queryParams()?.get('policy') ?? '');

  protected readonly match = computed(() => {
    const name = this.highlightName();
    if (!name) return undefined;
    return this.policies().find(p => p.name === name);
  });

  constructor() {
    this.policyService.listAll()
      .then(({ policies, errors }) => {
        this.policies.set(policies);
        this.sourceErrors.set(errors);
      })
      .catch(err => this.error.set(err instanceof Error ? err.message : String(err)))
      .finally(() => this.loading.set(false));
  }
}
