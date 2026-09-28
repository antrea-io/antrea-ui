// Copyright 2026 Antrea Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import { afterEach, expect, test } from 'vitest';
import { navigateTo } from './navigation';

afterEach(() => {
    window.history.replaceState({}, '', '/');
});

// A fragment-only URL is a same-document navigation, which jsdom does implement, so this
// exercises the real window.location without stubbing it.
test('navigateTo sets window.location.href', () => {
    navigateTo('#navigated');
    expect(window.location.hash).toBe('#navigated');
});
