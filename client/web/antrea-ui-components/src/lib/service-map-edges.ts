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

interface CountedEdge { source: string; target: string; connectionCount: number; }

/** Selects directed edges with stable ties and a 20% replacement margin.
 * Passing an empty previous set reranks immediately, e.g. after a scope or limit change.
 * A zero limit means all edges.
 */
export function busiestMapEdges(edges: CountedEdge[], limit: number, previous = new Set<string>()): Set<string> {
    const key = (e: CountedEdge) => `${e.source}|${e.target}`;
    const compare = (a: CountedEdge, b: CountedEdge) => b.connectionCount - a.connectionCount
        || (key(a) < key(b) ? -1 : key(a) > key(b) ? 1 : 0);
    const ranked = [...edges].sort(compare);
    if (!limit) return new Set(ranked.map(key));
    const kept = ranked.filter(e => previous.has(key(e))).slice(0, limit);
    for (const candidate of ranked) {
        if (previous.has(key(candidate)) && kept.includes(candidate)) continue;
        if (kept.length < limit) { kept.push(candidate); kept.sort(compare); continue; }
        if (candidate.connectionCount > kept[kept.length - 1].connectionCount * 1.2) {
            kept[kept.length - 1] = candidate;
            kept.sort(compare);
        }
    }
    return new Set(kept.map(key));
}
