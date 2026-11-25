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
import { state } from 'lit/decorators.js';
import { countPods, namespacePodItems } from './count-pods.js';
import type { NodeSelection } from '@antrea/ui-plugin-sdk';
import { access, registerNodeCardProvider, registerRoute, registerSidebarEntry } from '@antrea/ui-plugin-sdk';

const pagePath = '/plugin/pod-counter';
const listPods = { group: '', resource: 'pods', verb: 'list' };

// Minimal example plugin: shows the total number of pods in the cluster.
//
// There is no credential to receive from the host: requests to the Antrea UI backend
// authenticate with the session cookie the browser already holds, so `credentials: 'include'`
// is all that is needed. Note that the backend now calls Kubernetes as the *end user*, so this
// request can legitimately come back 403 if the viewing user has no RBAC for listing pods -
// unlike before, when every UI user had the same access.
class AntreaPluginPodCounter extends LitElement {
    @state() private _count: number | null = null;
    @state() private _error: string | null = null;
    private _request?: AbortController;

    connectedCallback() {
        super.connectedCallback();
        const request = new AbortController();
        this._request = request;
        this._count = null;
        this._error = null;
        countPods(undefined, request.signal)
            .then(count => { if (!request.signal.aborted) this._count = count; })
            .catch((e) => {
                if (!request.signal.aborted) this._error = e instanceof Error ? e.message : String(e);
            });
    }

    disconnectedCallback() {
        super.disconnectedCallback();
        this._request?.abort();
    }

    render() {
        if (this._error) return html`<p>Failed to load pod count: ${this._error}</p>`;
        return html`<h1>Pods in cluster: ${this._count ?? '...'}</h1>`;
    }
}

customElements.define('antrea-plugin-pod-counter', AntreaPluginPodCounter);

function nodeNamespace(node: NodeSelection): string | undefined {
    if (node.kind === 'workload' || node.kind === 'service' || node.kind === 'namespace') return node.namespace;
}

registerNodeCardProvider({
    id: 'pod-counter.namespace',
    requires: { namespaceScope: true, resources: [listPods] },
    appliesTo: node => !!nodeNamespace(node),
    getItems(node, signal) {
        const namespace = nodeNamespace(node);
        if (!namespace) return [];
        let permissions;
        try {
            permissions = access();
        } catch {
            // A host can support node cards without exposing its optional access API.
        }
        return namespacePodItems(namespace, signal, permissions?.can(listPods) ? pagePath : undefined);
    },
});

registerRoute({ path: pagePath, tag: 'antrea-plugin-pod-counter' });
registerSidebarEntry({
    label: 'Pod Counter',
    path: pagePath,
    icon: 'M7.752.066a.5.5 0 0 1 .496 0l3.75 2.143a.5.5 0 0 1 .252.434v3.995l3.498 2A.5.5 0 0 1 16 9.07v4.286a.5.5 0 0 1-.252.434l-3.75 2.143a.5.5 0 0 1-.496 0l-3.502-2-3.502 2.001a.5.5 0 0 1-.496 0l-3.75-2.143A.5.5 0 0 1 0 13.357V9.071a.5.5 0 0 1 .252-.434L3.75 6.638V2.643a.5.5 0 0 1 .252-.434zM4.25 7.504 1.508 9.071l2.742 1.567 2.742-1.567zM7.5 9.933l-2.75 1.571v3.134l2.75-1.571zm1 3.134 2.75 1.571v-3.134L8.5 9.933zm.508-3.996 2.742 1.567 2.742-1.567-2.742-1.567zm2.242-2.433V3.504L8.5 5.076V8.21zM7.5 8.21V5.076L4.75 3.504v3.134zM5.258 2.643 8 4.21l2.742-1.567L8 1.076zM15 9.933l-2.75 1.571v3.134L15 13.067zM3.75 14.638v-3.134L1 9.933v3.134z',
});
