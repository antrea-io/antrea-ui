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

import { Component, CUSTOM_ELEMENTS_SCHEMA, inject } from '@angular/core';
import { AuthService } from '../core/auth.service';

@Component({
  selector: 'app-login-page',
  standalone: true,
  template: `<antrea-login-page (antrea-token)="onToken($event)"></antrea-login-page>`,
  schemas: [CUSTOM_ELEMENTS_SCHEMA],
})
export class LoginPage {
  private readonly auth = inject(AuthService);

  onToken(e: Event): void {
    const detail = (e as CustomEvent<{ accessToken: string }>).detail;
    this.auth.setToken(detail.accessToken);
  }
}
