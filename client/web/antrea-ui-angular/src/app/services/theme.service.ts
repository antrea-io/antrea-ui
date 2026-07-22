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

import { Injectable, signal, computed } from '@angular/core';

export type Theme = 'dark' | 'light';

@Injectable({ providedIn: 'root' })
export class ThemeService {
  private readonly themeSignal = signal<Theme>('dark');

  readonly theme = this.themeSignal.asReadonly();
  readonly isDark = computed(() => this.themeSignal() === 'dark');

  constructor() {
    const saved = localStorage.getItem('ui.antrea.io/theme') as Theme | null;
    if (saved === 'light') {
      this._apply('light');
    }
  }

  toggle(): void {
    const next: Theme = this.themeSignal() === 'dark' ? 'light' : 'dark';
    this._apply(next);
  }

  private _apply(theme: Theme): void {
    this.themeSignal.set(theme);
    // index.html's static <body data-theme="dark"> never goes away on its own — a rule
    // matching an attribute directly on an element always wins over whatever that
    // element would otherwise inherit from <html>, so body must be updated explicitly
    // too, not just documentElement (that alone leaves body permanently stuck on dark).
    document.documentElement.setAttribute('data-theme', theme);
    document.body.setAttribute('data-theme', theme);
    document.body.setAttribute('cds-theme', theme);
    localStorage.setItem('ui.antrea.io/theme', theme);
  }
}
