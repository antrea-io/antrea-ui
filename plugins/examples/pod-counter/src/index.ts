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

import { LitElement, html } from 'lit';
import { property, state } from 'lit/decorators.js';

// Minimal example plugin: shows the total number of pods in the cluster. Contract with the
// host shell (see client/web/antrea-ui/src/plugins.ts): the host sets the `token` property to
// the current access token, same as it does for built-in pages.
class AntreaPluginPodCounter extends LitElement {
    @property() token = '';

    @state() private _count: number | null = null;
    @state() private _error: string | null = null;

    connectedCallback() {
        super.connectedCallback();
        fetch('/api/v1/k8s/api/v1/pods', {
            headers: { Authorization: `Bearer ${this.token}` },
        })
            .then((res) => {
                if (!res.ok) throw new Error(`request failed: ${res.status}`);
                return res.json();
            })
            .then((data) => {
                this._count = data.items?.length ?? 0;
            })
            .catch((e) => {
                this._error = e instanceof Error ? e.message : String(e);
            });
    }

    render() {
        if (this._error) return html`<p>Failed to load pod count: ${this._error}</p>`;
        return html`<h1>Pods in cluster: ${this._count ?? '...'}</h1>`;
    }
}

customElements.define('antrea-plugin-pod-counter', AntreaPluginPodCounter);
