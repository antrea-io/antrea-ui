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

import { Component, inject } from '@angular/core';
import { RouterOutlet } from '@angular/router';
import { ClarityModule } from '@clr/angular';
import { AuthService } from './core/auth.service';
import { Header } from './layout/header/header';
import { Nav } from './layout/nav/nav';
import { LoginPage } from './pages/login-page';

@Component({
  selector: 'app-root',
  standalone: true,
  imports: [RouterOutlet, ClarityModule, Header, Nav, LoginPage],
  templateUrl: './app.html',
  styleUrl: './app.css',
})
export class App {
  protected readonly auth = inject(AuthService);
}
