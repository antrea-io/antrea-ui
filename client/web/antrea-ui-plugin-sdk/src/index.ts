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

import { InjectionToken } from '@angular/core';
import type { Route } from '@angular/router';

/**
 * A sidebar entry a plugin wants the host shell to render, analogous to
 * Headlamp's registerSidebarEntry().
 */
export interface PluginSidebarEntry {
  label: string;
  path: string;
  icon: string;
  /** Second-level nav items rendered under this entry's label instead of a single link (e.g. a plugin's own sub-pages). */
  children?: PluginSidebarChildEntry[];
}

export interface PluginSidebarChildEntry {
  label: string;
  path: string;
}

/**
 * The API surface a host app passes to each plugin's register() function.
 * Plugins talk to the host only through this interface — they never import
 * host-internal services directly, so they stay buildable and testable as
 * standalone packages.
 */
export interface AntreaPluginHost {
  /** Analogous to Headlamp's registerRoute(). */
  registerRoute(route: Route): void;
  /** Analogous to Headlamp's registerSidebarEntry(). */
  registerSidebarEntry(entry: PluginSidebarEntry): void;
  /** Lets a plugin call the host's authenticated K8s API proxy without depending on the host's AuthService. */
  getAuthToken(): string | undefined;
}

/** DI token a plugin injects to obtain the AntreaPluginHost the host app provided. */
export const ANTREA_PLUGIN_HOST = new InjectionToken<AntreaPluginHost>('AntreaPluginHost');

/** The shape every plugin package's entry point (src/index.ts) must export. */
export type AntreaPluginRegister = (host: AntreaPluginHost) => void;
