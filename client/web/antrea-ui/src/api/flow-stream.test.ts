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

import { describe, expect, it } from 'vitest';
import { streamFilterKey } from './flow-stream';

describe('streamFilterKey', () => {
    it('matches for different object instances with the same filter', () => {
        const a = { follow: true as const };
        const b = { follow: true as const };
        expect(streamFilterKey(a)).toBe(streamFilterKey(b));
    });

    it('normalizes array field order', () => {
        const a = { follow: true as const, namespaces: ['z', 'a'] };
        const b = { follow: true as const, namespaces: ['a', 'z'] };
        expect(streamFilterKey(a)).toBe(streamFilterKey(b));
    });

    it('changes when a filter field changes', () => {
        const empty = { follow: true as const };
        const withNs = { follow: true as const, namespaces: ['default'] };
        expect(streamFilterKey(empty)).not.toBe(streamFilterKey(withNs));
    });
});
