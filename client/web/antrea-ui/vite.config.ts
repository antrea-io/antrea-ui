/**
 * Copyright 2023 Antrea Authors.
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

import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import { visualizer } from "rollup-plugin-visualizer";
import path from 'node:path'

// https://vitejs.dev/config/
export default defineConfig({
    resolve: {
        preserveSymlinks: true,
        // @antrea/ui-components's own package.json "exports" points at its built dist/ (needed
        // for consumers outside this Yarn workspace), which would otherwise make antrea-ui
        // resolve to a stale prebuilt bundle instead of picking up source edits. Send the bare
        // specifier straight to source instead, so `yarn start`/`yarn build` here reload on
        // changes to antrea-ui-components — a RegExp (not a plain string) so this doesn't also
        // catch the separate "./src/tokens.css" subpath export, which should keep resolving
        // normally.
        alias: [
            { find: /^@antrea\/ui-components$/, replacement: path.resolve(__dirname, '../antrea-ui-components/src/index.ts') },
        ],
    },
    build: {
        outDir: 'build',
    },
    plugins: [react(), visualizer()],
    server: {
        port: 3000,
        strictPort: true,
    },
})
