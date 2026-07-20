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

import { afterEach, describe, expect, test } from 'vitest';
import './antrea-input';
import type { AntreaInput } from './antrea-input';

let form: HTMLFormElement | null = null;

afterEach(() => {
    form?.remove();
    form = null;
});

function mountInForm(): { form: HTMLFormElement; input: AntreaInput } {
    form = document.createElement('form');
    const input = document.createElement('antrea-input') as AntreaInput;
    input.name = 'username';
    form.append(input);
    document.body.append(form);
    return { form, input };
}

describe('antrea-input — Enter-to-submit', () => {
    test('a real Enter keypress in the field submits the owning form', async () => {
        const { form, input } = mountInForm();
        await input.updateComplete;

        let submitted = false;
        form.addEventListener('submit', e => { e.preventDefault(); submitted = true; });

        const innerInput = input.shadowRoot!.querySelector('input')!;
        innerInput.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));

        expect(submitted).toBe(true);
    });

    test('other keys do not trigger a submit', async () => {
        const { form, input } = mountInForm();
        await input.updateComplete;

        let submitted = false;
        form.addEventListener('submit', () => { submitted = true; });

        const innerInput = input.shadowRoot!.querySelector('input')!;
        innerInput.dispatchEvent(new KeyboardEvent('keydown', { key: 'a', bubbles: true }));

        expect(submitted).toBe(false);
    });
});
