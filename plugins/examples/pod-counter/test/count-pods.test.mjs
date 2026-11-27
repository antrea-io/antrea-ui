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

import assert from 'node:assert/strict';
import test from 'node:test';
import { countPods, namespacePodItems } from '../.test-build/count-pods.js';

test('continuation requests preserve the original Pod list options', async t => {
    const signal = new AbortController().signal;
    const requests = [];
    const pages = [
        { items: [{}], metadata: { continue: 'next/+=' } },
        { items: [{}, {}], metadata: {} },
    ];
    t.mock.method(globalThis, 'fetch', async (path, options) => {
        const url = new URL(path, 'http://localhost');
        assert.equal(url.pathname, '/api/v1/k8s/api/v1/namespaces/shop/pods');
        assert.equal(options.signal, signal);
        requests.push(url.searchParams);
        return Response.json(pages[requests.length - 1]);
    });
    assert.equal(await countPods('shop', signal), 3);
    assert.equal(requests.length, 2);
    assert.equal(requests[1].get('continue'), 'next/+=');
    requests[1].delete('continue');
    assert.deepEqual([...requests[0]], [...requests[1]]);
});

test('uses the remaining item count without fetching another page', async t => {
    const fetch = t.mock.method(globalThis, 'fetch', async () =>
        Response.json({ items: [{}], metadata: { continue: 'unused', remainingItemCount: 9 } }));
    assert.equal(await countPods(), 10);
    assert.equal(fetch.mock.callCount(), 1);
});

test('the node card explains a namespaced denial and omits the cluster link', async t => {
    t.mock.method(globalThis, 'fetch', async () => new Response(null, { status: 403 }));
    const items = await namespacePodItems('shop', new AbortController().signal, '/plugin/pod-counter');
    assert.deepEqual(items, [{ kind: 'value', label: 'Pods in shop', value: 'you do not have permission to list Pods' }]);
});

test('closing an in-flight card does not produce an error row', async t => {
    const request = new AbortController();
    t.mock.method(globalThis, 'fetch', (_path, { signal }) => new Promise((_resolve, reject) => {
        signal.addEventListener('abort', () => reject(signal.reason), { once: true });
    }));
    const result = namespacePodItems('shop', request.signal);
    request.abort();
    assert.deepEqual(await result, []);
});
