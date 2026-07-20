/**
 * Copyright 2024 Antrea Authors.
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

import { defineConfig } from 'vitest/config'
import path from 'node:path'

export default defineConfig({
    resolve: {
        preserveSymlinks: true,
        // See the matching alias in vite.config.ts: sends the bare specifier to source instead
        // of the built dist/ that @antrea/ui-components' package.json "exports" points at, so
        // `yarn test` picks up antrea-ui-components changes without a rebuild there first.
        alias: [
            { find: /^@antrea\/ui-components$/, replacement: path.resolve(__dirname, '../antrea-ui-components/src/index.ts') },
        ],
    },
    test: {
        environment: 'jsdom',
        setupFiles: './src/setupTests.ts',
        globals: true,
    },
})
