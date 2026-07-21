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

import { Routes } from '@angular/router';
import { SummaryPage } from './pages/summary-page';
import { TraceflowPage } from './pages/traceflow-page';
import { FlowVisibilityPage } from './pages/flow-visibility-page';
import { SettingsPage } from './pages/settings-page';
import { pluginRoutes } from './plugins';

export const routes: Routes = [
  { path: '', pathMatch: 'full', redirectTo: 'summary' },
  { path: 'summary', component: SummaryPage },
  { path: 'traceflow', component: TraceflowPage },
  { path: 'flows', component: FlowVisibilityPage },
  ...pluginRoutes,
  { path: 'settings', component: SettingsPage },
];
