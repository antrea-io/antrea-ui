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

// Origin of the backend, if it's different from this frontend's own origin (e.g. local dev).
// Passed to antrea-ui-components' setApiBase() at startup; see src/index.tsx.
const apiServer = import.meta.env.VITE_API_SERVER || "";

// This shell's own full release version, injected at build time (VITE_ANTREA_UI_VERSION; see
// build/frontend.dockerfile), with any leading "v" dropped but any prerelease suffix (e.g.
// "-rc.2") kept - a plugin's compatibleAntreaUI range is matched against it (see plugins.ts).
// Empty in an unconfigured dev build, in which case that check is skipped.
const shellVersion = (import.meta.env.VITE_ANTREA_UI_VERSION || "").replace(/^v/, "");

const config = {
    apiServer: apiServer,
    shellVersion: shellVersion,
};

export default config;
