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

import type { AccessSummary } from './access-api.js';
import { allowsPluginItem, parseAccessRequirements, type PluginAccessRequirement } from './plugin-access.js';

/** Only the identity displayed by the graph. A workload label may be derived from a Pod name. */
export type NodeSelection =
    | { readonly kind: 'workload'; readonly label: string; readonly namespace?: string }
    | { readonly kind: 'service'; readonly name: string; readonly namespace: string }
    | { readonly kind: 'namespace'; readonly namespace: string }
    | { readonly kind: 'undisclosedNamespace'; readonly namespace: string }
    | { readonly kind: 'external' }
    | { readonly kind: 'undisclosed' };

export type NodeCardItem =
    | { readonly kind: 'value'; readonly label: string; readonly value: string }
    | { readonly kind: 'link'; readonly label: string; readonly href: string };

export interface NodeCardProvider {
    id: string;
    requires?: PluginAccessRequirement;
    /** Synchronous and side-effect free. The map evaluates this to make eligible nodes focusable. */
    appliesTo(node: NodeSelection): boolean;
    getItems(node: NodeSelection, signal: AbortSignal): readonly NodeCardItem[] | Promise<readonly NodeCardItem[]>;
}

/** Stores node card providers and filters them by the current user's access. */
export function createNodeCardRegistry() {
    const registrations = new Map<string, { provider: NodeCardProvider; access: readonly PluginAccessRequirement[] }>();
    type AccessState = { summary: AccessSummary | null; loaded: boolean };
    let cached: (AccessState & { providers: readonly NodeCardProvider[] }) | undefined;
    return {
        register(provider: NodeCardProvider) {
            if (typeof provider?.id !== 'string' || !provider.id.trim() ||
                typeof provider.appliesTo !== 'function' || typeof provider.getItems !== 'function') {
                console.error('Invalid plugin node card provider', provider);
                return;
            }
            if (registrations.has(provider.id)) {
                console.error(`Duplicate plugin node card provider "${provider.id}"`);
                return;
            }
            const access = parseAccessRequirements(provider.requires, `node card provider "${provider.id}"`);
            registrations.set(provider.id, { provider, access });
            cached = undefined;
        },
        visible(state: AccessState): readonly NodeCardProvider[] {
            // The map invalidates an open card when its access snapshot or registrations change.
            if (cached && cached.summary === state.summary && cached.loaded === state.loaded) return cached.providers;
            const providers = Array.from(registrations.values()).filter(r => allowsPluginItem(state, r.access)).map(r => r.provider);
            cached = { ...state, providers: Object.freeze(providers) };
            return cached.providers;
        },
    };
}

export function applicableProviders(providers: readonly NodeCardProvider[], node: NodeSelection): NodeCardProvider[] {
    return providers.filter(provider => {
        try {
            return provider.appliesTo(node) === true;
        } catch (error) {
            console.error(`Node card provider "${provider.id}" failed to match`, error);
            return false;
        }
    });
}

function validItems(items: readonly NodeCardItem[]): NodeCardItem[] {
    if (!Array.isArray(items)) return [];
    const rows: NodeCardItem[] = [];
    for (const item of items) {
        if (!item || typeof item.label !== 'string' || !item.label.trim()) continue;
        if (item.kind === 'value' && typeof item.value === 'string') {
            rows.push({ kind: 'value', label: item.label, value: item.value });
        } else if (item.kind === 'link' && typeof item.href === 'string' && item.href.trim()) {
            let url: URL;
            try {
                url = new URL(item.href, document.baseURI);
            } catch {
                continue;
            }
            if (url.protocol === 'http:' || url.protocol === 'https:') {
                rows.push({ kind: 'link', label: item.label, href: url.href });
            }
        }
    }
    return rows;
}

/** One slot per provider in registration order. An undefined slot is an outstanding request. */
export function loadNodeCardItems(providers: readonly NodeCardProvider[], node: NodeSelection,
    signal: AbortSignal, onUpdate: () => void): (NodeCardItem[] | undefined)[] {
    const slots = providers.map((provider, index) => {
        const failed = (error: unknown): NodeCardItem[] => {
            if (!signal.aborted) console.error(`Node card provider "${provider.id}" failed`, error);
            return [];
        };
        try {
            const result = provider.getItems(node, signal);
            if (Array.isArray(result)) return validItems(result);
            if (!result || !('then' in result) || typeof result.then !== 'function') return [];
            void Promise.resolve(result).then(validItems).catch(failed).then(items => {
                if (signal.aborted) return;
                slots[index] = items;
                onUpdate();
            });
            return undefined;
        } catch (error) {
            return failed(error);
        }
    });
    return slots;
}
