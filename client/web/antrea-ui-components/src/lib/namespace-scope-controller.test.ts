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


import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { resetAccessSummary } from './access-api.js';
import type { AccessSummary } from './access-api.js';
import { createNamespaceScope } from './namespace-scope-controller.js';
import type { NamespaceScopeHostAdapter, NamespaceScopeOptions } from './namespace-scope-controller.js';
import { createPluginNamespaceScope } from './plugin-namespace-scope.js';
import { SCOPE_CHANGE_EVENT, SCOPE_CHOSEN_EVENT } from './namespace-scope.js';

const PODS = [{ group: '', resource: 'pods', verb: 'list' }];

function summary(over: Partial<AccessSummary> = {}): AccessSummary {
    return {
        username: 'alice', groups: [], clusterAdmin: false, namespaces: ['ns-a', 'ns-b'],
        rules: { resourceRules: [], nonResourceRules: [], incomplete: false }, ...over,
    };
}

const clusterPods: Partial<AccessSummary> = {
    rules: {
        resourceRules: [{ verbs: ['list'], apiGroups: [''], resources: ['pods'] }],
        nonResourceRules: [], incomplete: false,
    },
};

let status = 200;
let body: unknown = summary();
let fetchMock: ReturnType<typeof vi.fn>;

beforeEach(() => {
    status = 200;
    body = summary();
    window.history.replaceState(null, '', '/');
    fetchMock = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify(body), { status })));
    vi.stubGlobal('fetch', fetchMock);
});

afterEach(() => {
    vi.unstubAllGlobals();
    resetAccessSummary();
});

function setup(over: Partial<NamespaceScopeOptions> = {}) {
    const target = new EventTarget();
    const chosen: string[] = [];
    const changes: string[] = [];
    let expired = 0;
    target.addEventListener(SCOPE_CHOSEN_EVENT, e => chosen.push((e as CustomEvent).detail.scope));
    target.addEventListener(SCOPE_CHANGE_EVENT, e => changes.push((e as CustomEvent).detail.search));
    target.addEventListener('antrea-session-expired', () => { expired++; });
    const handle = createNamespaceScope({ target, resources: PODS, ...over });
    return { target, handle, chosen, changes, expired: () => expired };
}

async function loaded(handle: ReturnType<typeof createNamespaceScope>) {
    await vi.waitFor(() => expect(handle.state().access).not.toBeNull());
}

describe('createNamespaceScope', () => {
    it('uses the scope of the URL before the access has loaded', () => {
        const { handle, chosen } = setup();
        handle.setLocationSearch('?namespace=ns-a');
        expect(handle.state().scope).toEqual({ kind: 'namespace', namespace: 'ns-a' });
        expect(handle.state().source).toBe('url');
        expect(handle.state().access).toBeNull();
        expect(chosen).toEqual(['ns-a']);
        handle.dispose();
    });

    it('maps _all to All Namespaces', () => {
        const { handle, chosen } = setup();
        handle.setLocationSearch('?namespace=_all');
        expect(handle.state().scope).toEqual({ kind: 'all' });
        expect(chosen).toEqual(['_all']);
        handle.dispose();
    });

    it('reports an invalid URL scope and selects nothing', async () => {
        const { handle, chosen } = setup();
        handle.setLocationSearch('?namespace=Not_Valid');
        await loaded(handle);
        expect(handle.state().scope).toBeNull();
        expect(handle.state().error).toContain('not a valid namespace name');
        expect(chosen).toEqual([]);
        handle.dispose();
    });

    it('reports the URL scope again once mounted', async () => {
        const { handle, chosen } = setup();
        handle.setLocationSearch('?namespace=ns-a');
        expect(chosen).toEqual(['ns-a']);
        await Promise.resolve();
        expect(chosen).toEqual(['ns-a', 'ns-a']);
        handle.dispose();
    });

    it('waits for the access to default, and neither reports nor writes the default', async () => {
        body = summary(clusterPods);
        const { handle, chosen, changes } = setup();
        handle.setLocationSearch('');
        expect(handle.state().scope).toBeNull();
        await loaded(handle);
        expect(handle.state().scope).toEqual({ kind: 'all' });
        expect(handle.state().source).toBe('default');
        expect(chosen).toEqual([]);
        expect(changes).toEqual([]);
        expect(window.location.search).toBe('');
        handle.dispose();
    });

    it('defaults to the first Namespace when All Namespaces is not allowed', async () => {
        const { handle } = setup();
        handle.setLocationSearch('');
        await loaded(handle);
        expect(handle.state().scope).toEqual({ kind: 'namespace', namespace: 'ns-a' });
        expect(handle.state().offerAll).toBe(false);
        handle.dispose();
    });

    it('does not default to All Namespaces on an unknown verdict', async () => {
        body = summary({ rules: { resourceRules: [], nonResourceRules: [], incomplete: true } });
        const { handle } = setup();
        handle.setLocationSearch('');
        await loaded(handle);
        expect(handle.state().access?.clusterWide).toBe('unknown');
        expect(handle.state().scope).toEqual({ kind: 'namespace', namespace: 'ns-a' });
        expect(handle.state().offerAll).toBe(true);
        handle.dispose();
    });

    it('takes the verdict from `resources`, and from clusterAdmin without them', async () => {
        const verdictOf = async (over: Partial<NamespaceScopeOptions>) => {
            const { handle } = setup(over);
            handle.setLocationSearch('');
            await loaded(handle);
            const v = handle.state().access?.clusterWide;
            handle.dispose();
            resetAccessSummary();
            return v;
        };
        expect(await verdictOf({})).toBe('denied');
        expect(await verdictOf({ resources: undefined })).toBe('unknown');
        body = summary({ clusterAdmin: true });
        expect(await verdictOf({ resources: undefined })).toBe('allowed');
    });

    it('keeps a picker choice made during loading and reports it only once', async () => {
        const { handle, chosen } = setup();
        handle.setLocationSearch('');
        handle.select({ kind: 'namespace', namespace: 'ns-b' });
        await loaded(handle);
        expect(handle.state().scope).toEqual({ kind: 'namespace', namespace: 'ns-b' });
        expect(handle.state().source).toBe('picker');
        expect(chosen).toEqual(['ns-b']);
        handle.dispose();
    });

    it('rejects a typed name that is not a Namespace name', async () => {
        window.history.replaceState(null, '', '/?namespace=ns-a');
        const { handle, chosen } = setup();
        handle.setLocationSearch('?namespace=ns-a');
        await loaded(handle);
        chosen.length = 0;
        handle.select({ kind: 'namespace', namespace: 'Bad Name' }, true);
        expect(handle.state().error).toBe('"Bad Name" is not a valid namespace name.');
        expect(handle.state().scope).toEqual({ kind: 'namespace', namespace: 'ns-a' });
        expect(chosen).toEqual([]);
        expect(window.location.search).toBe('?namespace=ns-a');
        handle.dispose();
    });

    it('rejects a typed All Namespaces sentinel, but accepts the picked one', async () => {
        const { handle } = setup();
        handle.setLocationSearch('?namespace=ns-a');
        handle.select({ kind: 'all' }, true);
        expect(handle.state().error).not.toBeNull();
        expect(handle.state().scope).toEqual({ kind: 'namespace', namespace: 'ns-a' });
        handle.select({ kind: 'all' }, false);
        expect(handle.state().error).toBeNull();
        expect(handle.state().scope).toEqual({ kind: 'all' });
        handle.dispose();
    });

    it('rejects a Namespace value that matches the internal All Namespaces sentinel', () => {
        const { handle, chosen, changes } = setup();
        handle.setLocationSearch('?namespace=ns-a');
        chosen.length = 0;
        handle.select({ kind: 'namespace', namespace: '__cluster_wide__' });
        expect(handle.state().scope).toEqual({ kind: 'namespace', namespace: 'ns-a' });
        expect(handle.state().error).toContain('not a valid namespace name');
        expect(chosen).toEqual([]);
        expect(changes).toEqual([]);
        handle.dispose();
    });

    it('writes and reports a pick once, and ignores the echo of its own write', async () => {
        const { handle, chosen, changes } = setup();
        handle.setLocationSearch('');
        await loaded(handle);
        chosen.length = 0;
        const seen: unknown[] = [];
        handle.subscribe(s => seen.push(s));
        handle.select({ kind: 'namespace', namespace: 'ns-b' });
        expect(window.location.search).toBe('?namespace=ns-b');
        expect(chosen).toEqual(['ns-b']);
        expect(changes).toEqual(['?namespace=ns-b']);
        expect(handle.state().source).toBe('picker');
        const notifications = seen.length;
        handle.setLocationSearch('?namespace=ns-b');
        expect(seen.length).toBe(notifications);
        expect(chosen).toEqual(['ns-b']);
        handle.dispose();
    });

    it('writes a pick of the default as a choice', async () => {
        const { handle, chosen } = setup();
        handle.setLocationSearch('');
        await loaded(handle);
        handle.select({ kind: 'namespace', namespace: 'ns-a' });
        expect(window.location.search).toBe('?namespace=ns-a');
        expect(chosen).toEqual(['ns-a']);
        expect(handle.state().source).toBe('picker');
        handle.dispose();
    });

    it('follows Back and Forward through setLocationSearch', async () => {
        const { handle } = setup();
        handle.setLocationSearch('?namespace=ns-a');
        handle.setLocationSearch('?namespace=ns-b');
        expect(handle.state().scope).toEqual({ kind: 'namespace', namespace: 'ns-b' });
        handle.setLocationSearch('?namespace=_all');
        expect(handle.state().scope).toEqual({ kind: 'all' });
        handle.setLocationSearch('?x=1');
        await loaded(handle);
        expect(handle.state().source).toBe('default');
        handle.dispose();
    });

    it('follows popstate when it is given no search', () => {
        const { handle } = setup();
        window.history.replaceState(null, '', '/?namespace=ns-a');
        handle.setLocationSearch(undefined);
        expect(handle.state().scope).toEqual({ kind: 'namespace', namespace: 'ns-a' });
        window.history.replaceState(null, '', '/?namespace=ns-b');
        window.dispatchEvent(new PopStateEvent('popstate'));
        expect(handle.state().scope).toEqual({ kind: 'namespace', namespace: 'ns-b' });
        handle.dispose();
        window.history.replaceState(null, '', '/?namespace=ns-a');
        window.dispatchEvent(new PopStateEvent('popstate'));
        expect(handle.state().scope).toEqual({ kind: 'namespace', namespace: 'ns-b' });
    });

    it('dispatches antrea-session-expired on a 401', async () => {
        status = 401;
        const { handle, expired } = setup();
        handle.setLocationSearch('');
        await vi.waitFor(() => expect(expired()).toBe(1));
        handle.dispose();
    });

    it('notes a selected Namespace that is not in the list', async () => {
        const { handle } = setup();
        handle.setLocationSearch('?namespace=ns-z');
        await vi.waitFor(() => expect(handle.state().note?.kind).toBe('not-listed'));
        handle.select({ kind: 'namespace', namespace: 'ns-a' });
        expect(handle.state().note).toBeNull();
        handle.dispose();
    });

    it.each(['list', 'get'])('reports an expired session on a Namespace %s', async verb => {
        body = summary({ namespaces: ['*'] });
        fetchMock.mockImplementation(async (url: string) => {
            if (url.endsWith('/access-summary')) return Response.json(body);
            if (verb === 'get' && url.includes('/namespaces?')) {
                return Response.json({ items: [{ metadata: { name: 'ns-a' } }] });
            }
            return new Response(null, { status: 401 });
        });
        const { handle, expired } = setup();
        handle.setLocationSearch(verb === 'get' ? '?namespace=ns-z' : '');
        await vi.waitFor(() => expect(expired()).toBe(1));
        handle.dispose();
    });

    it('keeps the session active when listing Namespaces is forbidden', async () => {
        fetchMock.mockResolvedValueOnce(Response.json(summary({ namespaces: ['*'] })))
            .mockResolvedValueOnce(new Response(null, { status: 403 }));
        const { handle, expired } = setup();
        handle.setLocationSearch('');
        await loaded(handle);
        expect(handle.state().access?.candidates).toBeNull();
        expect(expired()).toBe(0);
        handle.dispose();
    });

    it('does nothing after dispose', async () => {
        const { handle, chosen } = setup();
        handle.setLocationSearch('');
        const listener = vi.fn();
        handle.subscribe(listener);
        handle.dispose();
        await new Promise(resolve => setTimeout(resolve, 20));
        handle.select({ kind: 'namespace', namespace: 'ns-a' });
        expect(listener).not.toHaveBeenCalled();
        expect(handle.state().access).toBeNull();
        expect(chosen).toEqual([]);
        expect(window.location.search).toBe('');
    });

    it('computes a new default when the resources change', async () => {
        body = summary({
            rules: {
                resourceRules: [{ verbs: ['list'], apiGroups: [''], resources: ['services'] }],
                nonResourceRules: [], incomplete: false,
            },
        });
        const { handle } = setup();
        handle.setLocationSearch('');
        await loaded(handle);
        expect(handle.state().scope).toEqual({ kind: 'namespace', namespace: 'ns-a' });
        handle.setResources([{ group: '', resource: 'services', verb: 'list' }]);
        await vi.waitFor(() => expect(handle.state().scope).toEqual({ kind: 'all' }));
        handle.dispose();
    });

    it('retries a failed access summary when the query mode changes', async () => {
        status = 503;
        const { handle } = setup();
        handle.setLocationSearch('');
        await loaded(handle);
        expect(handle.state().scope).toBeNull();
        status = 200;
        handle.setResources([{ ...PODS[0], verb: 'watch' }]);
        await loaded(handle);
        expect(fetchMock).toHaveBeenCalledTimes(2);
        expect(handle.state().scope).toEqual({ kind: 'namespace', namespace: 'ns-a' });
        handle.dispose();
    });

    it('clears the old default synchronously when the permissions change', async () => {
        body = summary(clusterPods);
        const { handle, chosen, changes } = setup();
        handle.setLocationSearch('');
        await loaded(handle);
        expect(handle.state().scope).toEqual({ kind: 'all' });
        const seen: unknown[] = [];
        handle.subscribe(s => seen.push(s.scope));
        handle.setResources([{ group: '', resource: 'services', verb: 'list' }]);
        expect(handle.state().access).toBeNull();
        expect(handle.state().scope).toBeNull();
        // A URL update in the same turn must not default using the old resource's verdict.
        handle.setLocationSearch('?view=other');
        expect(handle.state().scope).toBeNull();
        await loaded(handle);
        expect(handle.state().scope).toEqual({ kind: 'namespace', namespace: 'ns-a' });
        expect(seen).not.toContainEqual({ kind: 'all' });
        expect(chosen).toEqual([]);
        expect(changes).toEqual([]);
        handle.dispose();
    });

    it.each(['url', 'picker'])('preserves an explicit %s scope while permissions change', async source => {
        body = summary(clusterPods);
        const { handle, chosen } = setup();
        handle.setLocationSearch(source === 'url' ? '?namespace=_all' : '');
        await loaded(handle);
        if (source === 'picker') handle.select({ kind: 'all' });
        chosen.length = 0;
        handle.setResources([{ group: '', resource: 'services', verb: 'list' }]);
        expect(handle.state().scope).toEqual({ kind: 'all' });
        await loaded(handle);
        expect(handle.state().access?.clusterWide).toBe('denied');
        expect(handle.state().scope).toEqual({ kind: 'all' });
        expect(handle.state().source).toBe(source);
        expect(chosen).toEqual([]);
        handle.dispose();
    });

    it('accepts anyOf and ignores equivalent resources from a fresh render', async () => {
        body = summary(clusterPods);
        const { handle } = setup({ resources: { anyOf: PODS } });
        handle.setLocationSearch('');
        await loaded(handle);
        expect(handle.state().scope).toEqual({ kind: 'all' });
        const listener = vi.fn();
        handle.subscribe(listener);
        handle.setResources(PODS.map(q => ({ ...q })));
        await Promise.resolve();
        expect(listener).not.toHaveBeenCalled();
        handle.dispose();
    });

    it('uses only the latest permissions when changed during a pending Namespace list', async () => {
        body = summary({ ...clusterPods, namespaces: ['*'] });
        let resolveList!: (response: Response) => void;
        const pending = new Promise<Response>(resolve => { resolveList = resolve; });
        fetchMock.mockImplementation((url: string) => String(url).includes('/k8s/api/v1/namespaces')
            ? pending : Promise.resolve(new Response(JSON.stringify(body), { status: 200 })));
        const { handle } = setup();
        handle.setLocationSearch('');
        await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
        handle.setResources(undefined);
        handle.setResources([{ group: '', resource: 'services', verb: 'list' }]);
        const seen: unknown[] = [];
        handle.subscribe(s => seen.push(s.scope));
        resolveList(new Response(JSON.stringify({ items: [{ metadata: { name: 'ns-a' } }] }), { status: 200 }));
        await loaded(handle);
        expect(handle.state().access?.clusterWide).toBe('denied');
        expect(seen).toEqual([{ kind: 'namespace', namespace: 'ns-a' }]);
        handle.dispose();
    });

    it('fetches a fresh Namespace list for a controller created after sign-out', async () => {
        body = summary({ namespaces: ['*'] });
        const names = { items: [{ metadata: { name: 'old-user-ns' } }] };
        fetchMock.mockImplementation((url: string) => Promise.resolve(new Response(
            JSON.stringify(String(url).includes('/k8s/api/v1/namespaces') ? names : body), { status: 200 })));
        const first = setup();
        first.handle.setLocationSearch('');
        await loaded(first.handle);
        expect(first.handle.state().access?.candidates).toEqual(['old-user-ns']);
        first.handle.dispose();

        resetAccessSummary();
        names.items = [{ metadata: { name: 'new-user-ns' } }];
        const second = setup();
        second.handle.setLocationSearch('');
        await loaded(second.handle);
        expect(second.handle.state().access?.candidates).toEqual(['new-user-ns']);
        second.handle.dispose();
    });
});

describe('createNamespaceScope with a host adapter', () => {
    function adapterSetup() {
        const adapter = {
            writeScope: vi.fn(), rememberScope: vi.fn(), sessionExpired: vi.fn(),
        } satisfies NamespaceScopeHostAdapter;
        const target = new EventTarget();
        const events = vi.fn();
        target.addEventListener(SCOPE_CHOSEN_EVENT, events);
        target.addEventListener(SCOPE_CHANGE_EVENT, events);
        target.addEventListener('antrea-session-expired', events);
        const handle = createNamespaceScope({ target, resources: PODS, adapter });
        return { adapter, events, handle };
    }

    it('calls the adapter instead of dispatching events or touching the URL', async () => {
        const { adapter, events, handle } = adapterSetup();
        handle.setLocationSearch('?namespace=ns-a&x=1');
        await loaded(handle);
        expect(adapter.rememberScope).toHaveBeenLastCalledWith('ns-a');
        handle.select({ kind: 'all' });
        expect(adapter.writeScope).toHaveBeenLastCalledWith('_all');
        expect(adapter.rememberScope).toHaveBeenLastCalledWith('_all');
        // What it wrote is not taken for a navigation when the host hands it back.
        const before = adapter.rememberScope.mock.calls.length;
        handle.setLocationSearch('?namespace=_all&x=1');
        expect(adapter.rememberScope.mock.calls.length).toBe(before);
        expect(events).not.toHaveBeenCalled();
        expect(window.location.search).toBe('');
        handle.dispose();
    });

    it('reports the URL scope again after mount, once the host has bound its adapter', async () => {
        const { adapter, handle } = adapterSetup();
        handle.setLocationSearch('?namespace=ns-a');
        expect(adapter.rememberScope).toHaveBeenCalledTimes(1);
        await Promise.resolve();
        expect(adapter.rememberScope).toHaveBeenCalledTimes(2);
        handle.dispose();
    });

    it('does not write the default', async () => {
        const { adapter, handle } = adapterSetup();
        handle.setLocationSearch('');
        await loaded(handle);
        expect(adapter.writeScope).not.toHaveBeenCalled();
        expect(adapter.rememberScope).not.toHaveBeenCalled();
        handle.dispose();
    });

    it('tells the adapter the session expired', async () => {
        status = 401;
        const { adapter, events, handle } = adapterSetup();
        handle.setLocationSearch('');
        await vi.waitFor(() => expect(adapter.sessionExpired).toHaveBeenCalledTimes(1));
        expect(events).not.toHaveBeenCalled();
        handle.dispose();
    });

    it('keeps the host adapter and lifecycle even when a plugin passes internal options', async () => {
        status = 401;
        const adapter = { writeScope: vi.fn(), rememberScope: vi.fn(), sessionExpired: vi.fn() };
        const internal = { adapter: undefined, onSessionExpired: vi.fn(), isActive: () => false };
        const handle = createPluginNamespaceScope(adapter).create({ target: new EventTarget(), ...internal });
        handle.setLocationSearch('?namespace=ns-a');
        await loaded(handle);
        handle.select({ kind: 'namespace', namespace: 'ns-b' });
        expect(adapter.sessionExpired).toHaveBeenCalledTimes(1);
        expect(internal.onSessionExpired).not.toHaveBeenCalled();
        expect(adapter.writeScope).toHaveBeenCalledWith('ns-b');
        expect(adapter.rememberScope).toHaveBeenLastCalledWith('ns-b');
        expect(window.location.search).toBe('');
        handle.dispose();
    });
});
