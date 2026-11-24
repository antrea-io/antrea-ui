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

import { describe, expect, test } from 'vitest';
import { busiestMapEdges } from './service-map-edges';

const edge = (source: string, connectionCount: number) => ({ source, target: 'target', connectionCount });

describe('busiest directed map edges', () => {
    test('ranks counts with stable key ties, independent of input order', () => {
        const edges = [edge('c', 2), edge('b', 2), edge('a', 1)];
        expect([...busiestMapEdges(edges, 1)]).toEqual(['b|target']);
        expect(busiestMapEdges(edges.reverse(), 1)).toEqual(new Set(['b|target']));
        expect(busiestMapEdges(edges, 0).size).toBe(3);
    });
    test('requires more than 20% advantage to replace a retained edge and removes stale keys', () => {
        const previous = new Set(['a|target', 'gone|target']);
        expect(busiestMapEdges([edge('a', 10), edge('b', 12)], 1, previous)).toEqual(new Set(['a|target']));
        expect(busiestMapEdges([edge('a', 10), edge('b', 13)], 1, previous)).toEqual(new Set(['b|target']));
        expect(busiestMapEdges([edge('b', 1)], 1, previous)).toEqual(new Set(['b|target']));
        expect(busiestMapEdges([edge('a', 10), edge('b', 12)], 1)).toEqual(new Set(['b|target']));
    });
});
