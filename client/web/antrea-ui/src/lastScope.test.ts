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

import { afterEach, describe, expect, test } from 'vitest';
import { getLastScope, setLastScope } from './lastScope';

afterEach(() => sessionStorage.clear());

describe('last scope chosen', () => {
    test('is kept for the user who chose it and not offered to another', () => {
        setLastScope('alice', 'ns-a');
        expect(getLastScope('alice')).toBe('ns-a');
        expect(getLastScope('bob')).toBeNull();
    });

    test('is not stored when the user is not known', () => {
        setLastScope('', 'ns-a');
        expect(sessionStorage.length).toBe(0);
    });

    test('is not read when the user is not known', () => {
        sessionStorage.setItem('antrea-ui:last-scope', JSON.stringify({ user: '', scope: 'ns-a' }));
        expect(getLastScope('')).toBeNull();
    });
});
