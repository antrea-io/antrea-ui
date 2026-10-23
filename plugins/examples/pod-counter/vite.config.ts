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

import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { defineConfig, type Plugin } from 'vite';

// Writes manifest.json into dist with compatibleAntreaUI covering every version of the Antrea UI
// minor release in the repository's VERSION file (prereleases of its .0 included). This example is
// only ever built and tested together with the frontend from the same tree, so its range follows
// the tree instead of being bumped by hand.
function manifest(): Plugin {
  return {
    name: 'manifest',
    generateBundle() {
      const version = readFileSync(resolve(__dirname, '../../../VERSION'), 'utf8').trim().replace(/^v/, '');
      const match = version.match(/^(\d+)\.(\d+)\./);
      if (!match) {
        throw new Error(`cannot parse VERSION "${version}"`);
      }
      const [, major, minor] = match;
      const m = JSON.parse(readFileSync(resolve(__dirname, 'manifest.json'), 'utf8'));
      m.compatibleAntreaUI = `>=${major}.${minor}.0-0 <${major}.${Number(minor) + 1}.0`;
      this.emitFile({ type: 'asset', fileName: 'manifest.json', source: JSON.stringify(m, null, 2) + '\n' });
    },
  };
}

export default defineConfig({
  plugins: [manifest()],
  build: {
    lib: {
      entry: resolve(__dirname, 'src/index.ts'),
      fileName: 'index',
      formats: ['es'],
    },
    // Unlike @antrea/ui-components (which externalizes lit and expects the host app to provide
    // it), this bundle is import()ed directly by the browser at runtime with no host-provided
    // import map, so its dependencies must be bundled in.
    rollupOptions: {},
  },
});
