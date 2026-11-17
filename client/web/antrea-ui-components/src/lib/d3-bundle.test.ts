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

import { execFileSync } from 'node:child_process';
import { resolve } from 'node:path';
import { execPath } from 'node:process';
import { build } from 'vite';
import { expect, it } from 'vitest';

it('initializes D3 zoom on the selection used by a production bundle', async () => {
    const entry = resolve(import.meta.dirname, 'd3-bundle-probe.js');
    const result = await build({
        root: import.meta.dirname,
        configFile: false,
        logLevel: 'silent',
        plugins: [{
            name: 'd3-bundle-probe',
            resolveId(id) {
                if (id === entry) return entry;
            },
            load(id) {
                if (id !== entry) return;
                // Match the Service Map's initial zoom transform. An empty selection needs
                // no DOM, but still exercises d3-transition's selection.interrupt patch.
                return `import * as d3 from 'd3';
                    d3.zoom().transform(d3.select(null), d3.zoomIdentity);`;
            },
        }],
        build: {
            write: false,
            minify: true,
            lib: { entry, formats: ['es'] },
        },
    });
    const output = Array.isArray(result) ? result[0] : result;
    if (!('output' in output)) throw new Error('Expected a production bundle');
    const chunk = output.output.find(item => item.type === 'chunk' && item.isEntry);
    if (!chunk || chunk.type !== 'chunk') throw new Error('Missing production entry chunk');

    // Run only the bundled code in a fresh process: unbundled D3 imports in the test
    // runner would retain side effects that production tree shaking can remove.
    expect(() => execFileSync(execPath, ['--input-type=module'], {
        input: chunk.code,
        stdio: ['pipe', 'pipe', 'pipe'],
    })).not.toThrow();
}, 30_000);
