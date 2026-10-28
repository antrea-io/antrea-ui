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

import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';
import { resetAccessSummary } from '../lib/access-api.js';
import { setApiBase } from '../lib/api.js';
import type { SupportBundle } from '../lib/supportbundle-api.js';
import './antrea-support-bundle-page.js';
import type { AntreaSupportBundlePage } from './antrea-support-bundle-page.js';

const bundle: SupportBundle = {
    id: '5f0c6c1e-2b8e-4a40-9c4e-0c1f1c1d2e3f', status: 'Collected',
    createdAt: '2026-10-28T13:00:00Z', expiresAt: '2026-10-28T19:00:00Z',
    createdBy: 'alice', size: 2048,
};
const collecting: SupportBundle = { ...bundle, status: 'Collecting', size: undefined };
const basePath = '/api/v1/supportbundle';
let el: AntreaSupportBundlePage;
let hidden = false;

function json(body: unknown, status = 200) {
    return new Response(JSON.stringify(body), { status });
}

function stubAPI(
    handler: (url: string, init: RequestInit) => Response | Promise<Response> = () => json({ items: [bundle] }),
    verbs = ['list', 'create', 'get', 'delete'],
    accessResponse?: () => Response | Promise<Response>,
) {
    const fetchMock = vi.fn(async (url: string, init: RequestInit = {}) => {
        if (url.endsWith('/access-summary')) {
            return accessResponse ? accessResponse() : json({
                username: 'alice', groups: [], clusterAdmin: false, namespaces: [],
                rules: {
                    resourceRules: [{ apiGroups: ['ui.antrea.io'], resources: ['supportbundles'], verbs }],
                    nonResourceRules: [], incomplete: false,
                },
            });
        }
        return handler(url, init);
    });
    vi.stubGlobal('fetch', fetchMock);
    return fetchMock;
}

async function flush() {
    await vi.advanceTimersByTimeAsync(0);
    await el.updateComplete;
}

async function mount(expired = vi.fn()) {
    el = document.createElement('antrea-support-bundle-page');
    el.addEventListener('antrea-session-expired', expired);
    document.body.appendChild(el);
    await flush();
    return el.shadowRoot!;
}

function click(selector: string) {
    const target = el.shadowRoot!.querySelector<HTMLElement>(selector)!;
    const button = target.shadowRoot?.querySelector('button');
    (button ?? target).click();
}

function submit(since = '') {
    const input = el.shadowRoot!.querySelector<HTMLInputElement>('#since')!;
    input.value = since;
    input.dispatchEvent(new Event('input', { bubbles: true }));
    click('#collect');
}

function visibility(value: boolean) {
    hidden = value;
    document.dispatchEvent(new Event('visibilitychange'));
}

beforeEach(() => {
    vi.useFakeTimers();
    hidden = false;
    vi.spyOn(document, 'hidden', 'get').mockImplementation(() => hidden);
    resetAccessSummary();
});

afterEach(() => {
    el?.remove();
    resetAccessSummary();
    setApiBase('');
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    vi.useRealTimers();
});

describe('support bundle listing and permissions', () => {
    test('shows metadata, failed bundle errors, and partial source outcomes', async () => {
        stubAPI(() => json({ items: [{ ...bundle, sources: [
            { name: 'plugin-a', kind: 'plugin', status: 'Collected', size: 512 },
            { name: 'service-b', kind: 'extra', status: 'Failed', error: 'source unavailable' },
        ] }, { ...bundle, id: 'failed', status: 'Failed', error: 'disk budget exceeded' }] }));
        const root = await mount();
        expect(root.textContent).toContain(bundle.id);
        expect(root.textContent).toContain('alice');
        expect(root.textContent).toContain('2,048 B');
        expect(root.querySelector('time')?.getAttribute('datetime')).toBe(bundle.createdAt);
        expect(root.querySelector(`time[datetime="${bundle.expiresAt}"]`)).not.toBeNull();
        expect(root.textContent).toContain('Partial bundle');
        expect(root.querySelector('details')?.textContent).toContain('source unavailable');
        expect(root.querySelector('details')?.textContent).toContain('plugin-a');
        expect(root.textContent).toContain('disk budget exceeded');
        expect(root.querySelectorAll('a[download]')).toHaveLength(1);
    });

    test('accepts omitted optional fields and an empty list', async () => {
        const fetchMock = stubAPI(() => json({ items: [collecting] }));
        await mount();
        fetchMock.mockResolvedValue(json({ items: [] }));
        click('#refresh');
        await flush();
        expect(el.shadowRoot!.textContent).toContain('No support bundles');
        expect(el.shadowRoot!.querySelector('table')).toBeNull();
    });

    test.each([
        ['list'], ['list', 'create'], ['list', 'get'], ['list', 'delete'],
    ])('gates each action separately for %j', async (...verbs) => {
        stubAPI(undefined, verbs);
        const root = await mount();
        expect(!!root.querySelector('#collect')).toBe(verbs.includes('create'));
        expect(!!root.querySelector('a[download]')).toBe(verbs.includes('get'));
        expect(root.querySelector('.actions')?.textContent?.includes('Delete')).toBe(verbs.includes('delete'));
    });

    test('a denied list gate prevents all bundle requests, even with action grants', async () => {
        const fetchMock = stubAPI(undefined, ['create', 'get', 'delete']);
        const root = await mount();
        expect(root.textContent).toContain('You do not have permission');
        expect(fetchMock).toHaveBeenCalledTimes(1);
    });

    test('waits for the access summary before rendering actions or requesting data', async () => {
        let resolve!: (response: Response) => void;
        const fetchMock = stubAPI(undefined, undefined, () => new Promise(r => { resolve = r; }));
        const root = await mount();
        expect(root.querySelector('#collect')).toBeNull();
        expect(fetchMock).toHaveBeenCalledTimes(1);
        resolve(json({ clusterAdmin: true }));
        await flush();
        expect(root.querySelector('#collect')).not.toBeNull();
    });

    test('fails open when access is unknown, leaving authorization to the API', async () => {
        stubAPI(undefined, undefined, () => json('review unavailable', 500));
        const root = await mount();
        expect(root.querySelector('#collect')).not.toBeNull();
    });

    test('explains a disabled deployment and disables actions until a successful refresh', async () => {
        const fetchMock = stubAPI(() => json({ error: 'disabled' }, 501));
        const root = await mount();
        expect(root.textContent).toContain('Support bundles are not enabled for this deployment.');
        expect(root.querySelector('#collect')?.hasAttribute('disabled')).toBe(true);
        fetchMock.mockResolvedValue(json({ items: [] }));
        click('#refresh');
        await flush();
        expect(root.querySelector('#collect')?.hasAttribute('disabled')).toBe(false);
    });
});

describe('support bundle collection', () => {
    test.each(['', ' 1h30m '])('posts optional since=%j and immediately lists the new bundle', async since => {
        const fetchMock = stubAPI((_url, init) => init.method === 'POST' ? json(collecting, 202) : json({ items: [] }));
        const root = await mount();
        submit(since);
        await flush();
        const posts = fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST');
        expect(posts).toHaveLength(1);
        expect(posts[0][0]).toBe(basePath);
        expect(posts[0][1]?.credentials).toBe('include');
        expect(posts[0][1]?.body).toBe(since ? JSON.stringify({ since: since.trim() }) : undefined);
        expect(root.textContent).toContain(bundle.id);
        expect(root.textContent).toContain('Collecting');
        expect(root.querySelector('a[download]')).toBeNull();
    });

    test.each([
        [400, 'since must be a positive duration', 'since must be a positive duration'],
        [429, 'maximum number of bundles retained', 'maximum number of bundles retained'],
        [429, null, 'shared hourly support bundle collection limit'],
        [403, 'access denied', 'access denied'],
    ])('decodes collection error %i / %j', async (status, body, message) => {
        stubAPI((_url, init) => init.method === 'POST'
            ? body === null ? new Response(null, { status }) : json(body, status)
            : json({ items: [] }));
        const root = await mount();
        submit('invalid');
        await flush();
        const error = root.querySelector('antrea-alert[status="danger"]')!.textContent!.trim();
        expect(error).toContain(message);
        expect(error.startsWith('"')).toBe(false);
        expect(root.querySelector('#collect')?.hasAttribute('disabled')).toBe(false);
    });

    test('does not submit twice while a collection request is pending', async () => {
        let resolve!: (response: Response) => void;
        const fetchMock = stubAPI((_url, init) => init.method === 'POST'
            ? new Promise(r => { resolve = r; }) : json({ items: [] }));
        await mount();
        submit();
        submit();
        await flush();
        expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
        resolve(json(collecting, 202));
        await flush();
    });

    test('a rejected collection preserves its error while existing bundles keep updating', async () => {
        const fetchMock = stubAPI((_url, init) => init.method === 'POST'
            ? json('since must be a positive duration', 400) : json({ items: [collecting] }));
        const root = await mount();
        submit('invalid');
        await flush();
        await vi.advanceTimersByTimeAsync(2000);
        expect(fetchMock).toHaveBeenCalledTimes(4);
        expect(root.querySelector('antrea-alert[status="danger"]')?.textContent)
            .toContain('since must be a positive duration');
    });
});

describe('support bundle polling', () => {
    test('re-lists for a list-only user, removes missing bundles, and stops once collection finishes', async () => {
        let items = [collecting, { ...collecting, id: 'deleted' }];
        const fetchMock = stubAPI(() => json({ items }), ['list']);
        const root = await mount();
        items = [bundle];
        await vi.advanceTimersByTimeAsync(2000);
        expect(root.textContent).toContain('Collected');
        expect(root.querySelector('[data-bundle-id="deleted"]')).toBeNull();
        expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
            '/api/v1/access-summary', basePath, basePath,
        ]);
        await vi.advanceTimersByTimeAsync(20_000);
        expect(fetchMock).toHaveBeenCalledTimes(3);
    });

    test('pauses in a hidden tab and refreshes on return', async () => {
        const fetchMock = stubAPI(() => json({ items: [collecting] }));
        await mount();
        visibility(true);
        await vi.advanceTimersByTimeAsync(20_000);
        expect(fetchMock).toHaveBeenCalledTimes(2);
        visibility(false);
        await flush();
        expect(fetchMock).toHaveBeenCalledTimes(3);
        await vi.advanceTimersByTimeAsync(2000);
        expect(fetchMock).toHaveBeenCalledTimes(4);
    });

    test('does not start listing when mounted in a hidden tab', async () => {
        hidden = true;
        const fetchMock = stubAPI();
        await mount();
        expect(fetchMock).toHaveBeenCalledTimes(1);
        visibility(false);
        await flush();
        expect(fetchMock).toHaveBeenCalledTimes(2);
    });

    test('an in-flight response arriving in a hidden tab does not restart polling', async () => {
        let resolve!: (response: Response) => void;
        const fetchMock = stubAPI(() => new Promise(r => { resolve = r; }));
        await mount();
        visibility(true);
        resolve(json({ items: [collecting] }));
        await vi.advanceTimersByTimeAsync(20_000);
        expect(fetchMock).toHaveBeenCalledTimes(2);
    });

    test('preserves expanded sources when the list updates', async () => {
        stubAPI(() => json({ items: [{ ...collecting, sources: [
            { name: 'p', kind: 'plugin', status: 'Collecting' },
        ] }] }));
        const root = await mount();
        root.querySelector('details')!.open = true;
        await vi.advanceTimersByTimeAsync(2000);
        expect(root.querySelector('details')!.open).toBe(true);
    });

    test('aborts in-flight requests on unmount and ignores their late responses', async () => {
        let resolve!: (response: Response) => void;
        const fetchMock = stubAPI(() => new Promise(r => { resolve = r; }));
        await mount();
        const signal = fetchMock.mock.calls[1][1]?.signal;
        el.remove();
        expect(signal?.aborted).toBe(true);
        resolve(json({ items: [collecting] }));
        await vi.advanceTimersByTimeAsync(20_000);
        expect(fetchMock).toHaveBeenCalledTimes(2);
        expect(el.shadowRoot!.querySelector('table')).toBeNull();
    });

    test('does not overlap slow re-lists with a mutation or another poll', async () => {
        let resolve!: (response: Response) => void;
        let lists = 0;
        const fetchMock = stubAPI(() => ++lists === 1 ? json({ items: [collecting] })
            : new Promise(r => { resolve = r; }));
        await mount();
        await vi.advanceTimersByTimeAsync(2000);
        submit();
        await vi.advanceTimersByTimeAsync(10_000);
        expect(fetchMock).toHaveBeenCalledTimes(3);
        resolve(json({ items: [] }));
        await flush();
    });

    test('times out a stalled request and makes manual retry available', async () => {
        const fetchMock = stubAPI((_url, init) => new Promise((_resolve, reject) => {
            init.signal!.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')));
        }));
        const root = await mount();
        await vi.advanceTimersByTimeAsync(30_000);
        expect(root.textContent).toContain('request timed out');
        expect(root.querySelector('#refresh')?.hasAttribute('disabled')).toBe(false);
        fetchMock.mockResolvedValue(json({ items: [] }));
        click('#refresh');
        await flush();
        expect(root.textContent).toContain('No support bundles');
    });

    test('stops automatic retries after a list error and allows manual recovery', async () => {
        let fail = false;
        const fetchMock = stubAPI(() => fail ? json('forbidden', 403) : json({ items: [collecting] }));
        const root = await mount();
        fail = true;
        await vi.advanceTimersByTimeAsync(2000);
        expect(root.textContent).toContain('forbidden');
        await vi.advanceTimersByTimeAsync(20_000);
        expect(fetchMock).toHaveBeenCalledTimes(3);
        fail = false;
        click('#refresh');
        await flush();
        await vi.advanceTimersByTimeAsync(2000);
        expect(fetchMock).toHaveBeenCalledTimes(5);
    });
});

describe('support bundle download and deletion', () => {
    test.each(['', 'http://localhost:8080'])('preflights metadata, then uses a native download with API base %j', async base => {
        setApiBase(base);
        const fetchMock = stubAPI(url => url.endsWith('/status') ? json(bundle) : json({ items: [bundle] }));
        const root = await mount();
        const link = root.querySelector<HTMLAnchorElement>('a[download]')!;
        expect(link.getAttribute('href')).toBe(`${base}${basePath}/${bundle.id}/download`);
        const downloads: string[] = [];
        vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
            downloads.push(this.href);
            expect(this.hasAttribute('download')).toBe(true);
            expect(this.isConnected).toBe(true);
        });
        link.dispatchEvent(new MouseEvent('click', { cancelable: true }));
        await flush();
        expect(downloads).toHaveLength(1);
        expect(downloads[0]).toContain(`${basePath}/${bundle.id}/download`);
        expect(fetchMock.mock.calls.at(-1)?.[0]).toBe(`${base}${basePath}/${bundle.id}/status`);
        expect(fetchMock.mock.calls.some(([url]) => url.endsWith('/download'))).toBe(false);
        expect(document.querySelector('iframe')).toBeNull();
    });

    test.each([404, 403, 401])('preflight %i never starts a download', async status => {
        stubAPI(url => url.endsWith('/status') ? json('unavailable', status) : json({ items: [bundle] }));
        const expired = vi.fn();
        const root = await mount(expired);
        const nativeClick = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
        root.querySelector('a')!.dispatchEvent(new MouseEvent('click', { cancelable: true }));
        await flush();
        expect(nativeClick).not.toHaveBeenCalled();
        expect(expired).toHaveBeenCalledTimes(status === 401 ? 1 : 0);
        expect(root.querySelector('[data-bundle-id]') === null).toBe(status !== 403);
        expect(!!root.querySelector('antrea-alert[status="danger"]')).toBe(status === 403);
    });

    test('updates a stale collected bundle without downloading when it has failed', async () => {
        stubAPI(url => url.endsWith('/status') ? json({ ...bundle, status: 'Failed', error: 'restarted' })
            : json({ items: [bundle] }));
        const root = await mount();
        const nativeClick = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
        root.querySelector('a')!.dispatchEvent(new MouseEvent('click', { cancelable: true }));
        await flush();
        expect(nativeClick).not.toHaveBeenCalled();
        expect(root.textContent).toContain('restarted');
        expect(root.querySelector('a[download]')).toBeNull();
    });

    test('requires confirmation and explains cancellation before deleting for all users', async () => {
        const fetchMock = stubAPI((_url, init) => init.method === 'DELETE'
            ? new Response(null, { status: 200 }) : json({ items: [collecting] }));
        const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
        const root = await mount();
        click('.actions antrea-button');
        await flush();
        expect(fetchMock).toHaveBeenCalledTimes(2);
        confirm.mockReturnValue(true);
        click('.actions antrea-button');
        await flush();
        expect(confirm).toHaveBeenLastCalledWith(expect.stringContaining('cancels its collection'));
        expect(fetchMock.mock.calls.at(-1)?.[0]).toBe(`${basePath}/${bundle.id}`);
        expect(fetchMock.mock.calls.at(-1)?.[1]?.method).toBe('DELETE');
        expect(root.querySelector('[data-bundle-id]')).toBeNull();
        expect(root.textContent).toContain('50 seconds');
        await vi.advanceTimersByTimeAsync(20_000);
        expect(fetchMock).toHaveBeenCalledTimes(3);
    });

    test('a delete 404 removes a stale row', async () => {
        stubAPI((_url, init) => init.method === 'DELETE' ? json('not found', 404) : json({ items: [bundle] }));
        vi.spyOn(window, 'confirm').mockReturnValue(true);
        const root = await mount();
        click('.actions antrea-button');
        await flush();
        expect(root.querySelector('[data-bundle-id]')).toBeNull();
        expect(root.textContent).toContain('deleted or has expired');
    });
});

test.each(['access', 'list', 'collect', 'delete'])('a %s 401 ends the session and stops polling', async action => {
    const expired = vi.fn();
    const fetchMock = stubAPI((_url, init) => {
        if (action === 'list' || action === 'collect' && init.method === 'POST' ||
            action === 'delete' && init.method === 'DELETE') return json('expired', 401);
        return json({ items: [collecting] });
    }, undefined, action === 'access' ? () => json('expired', 401) : undefined);
    await mount(expired);
    if (action === 'collect') submit();
    if (action === 'delete') {
        vi.spyOn(window, 'confirm').mockReturnValue(true);
        click('.actions antrea-button');
    }
    await flush();
    expect(expired).toHaveBeenCalledTimes(1);
    const count = fetchMock.mock.calls.length;
    await vi.advanceTimersByTimeAsync(20_000);
    expect(fetchMock).toHaveBeenCalledTimes(count);
});
