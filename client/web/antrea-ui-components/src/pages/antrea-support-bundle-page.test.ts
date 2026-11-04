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

function submit() {
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
        expect(root.textContent).toContain('2.0 KB');
        expect(root.querySelector('time')?.getAttribute('datetime')).toBe(bundle.createdAt);
        expect(root.querySelector(`time[datetime="${bundle.expiresAt}"]`)).not.toBeNull();
        expect(root.textContent).toContain('Partial bundle');
        expect(root.querySelector('details')?.textContent).toContain('source unavailable');
        expect(root.querySelector('details')?.textContent).toContain('plugin-a');
        expect(root.textContent).toContain('disk budget exceeded');
        expect(root.querySelectorAll('a[download]')).toHaveLength(1);
    });

    test('accepts omitted optional fields and an empty list', async () => {
        const fetchMock = stubAPI(() => json({ items: [{ ...collecting, createdBy: undefined }] }));
        const root = await mount();
        const cells = [...root.querySelectorAll('tbody td')].map(td => td.textContent?.trim());
        expect(cells.filter(text => text === '—')).toHaveLength(2);
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
    test('posts a bodiless request and immediately lists the new bundle', async () => {
        const fetchMock = stubAPI((_url, init) => init.method === 'POST' ? json(collecting, 202) : json({ items: [] }));
        const root = await mount();
        submit();
        await flush();
        const posts = fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST');
        expect(posts).toHaveLength(1);
        expect(posts[0][0]).toBe(basePath);
        expect(posts[0][1]?.credentials).toBe('include');
        expect(posts[0][1]?.body).toBeUndefined();
        expect(root.textContent).toContain(bundle.id);
        expect(root.textContent).toContain('Collecting');
        expect(root.querySelector('a[download]')).toBeNull();
    });

    test.each([
        [429, 'maximum number of bundles retained', 'maximum number of bundles retained'],
        [429, null, 'shared hourly support bundle collection limit'],
        [403, 'access denied', 'access denied'],
    ])('decodes collection error %i / %j', async (status, body, message) => {
        stubAPI((_url, init) => init.method === 'POST'
            ? body === null ? new Response(null, { status }) : json(body, status)
            : json({ items: [] }));
        const root = await mount();
        submit();
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
        const root = await mount();
        submit();
        root.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }));
        await flush();
        expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(1);
        resolve(json(collecting, 202));
        await flush();
    });

    test('does not submit while the deployment is disabled', async () => {
        const fetchMock = stubAPI(() => json({ error: 'disabled' }, 501));
        const root = await mount();
        root.querySelector('form')!.dispatchEvent(new Event('submit', { cancelable: true }));
        await flush();
        expect(fetchMock.mock.calls.some(([, init]) => init?.method === 'POST')).toBe(false);
    });

    test('lists a collection that a poll already returned only once', async () => {
        stubAPI((_url, init) => init.method === 'POST' ? json(collecting, 202) : json({ items: [collecting] }));
        const root = await mount();
        submit();
        await flush();
        expect(root.querySelectorAll('[data-bundle-id]')).toHaveLength(1);
    });

    test('a new collection drops the previous action notice', async () => {
        stubAPI((_url, init) => init.method === 'POST' ? json('access denied', 403)
            : init.method === 'DELETE' ? new Response(null, { status: 200 }) : json({ items: [bundle] }));
        vi.spyOn(window, 'confirm').mockReturnValue(true);
        const root = await mount();
        click('.actions antrea-button');
        await flush();
        expect(root.textContent).toContain('Bundle deleted for all users.');
        submit();
        await flush();
        expect(root.textContent).not.toContain('Bundle deleted');
        expect(root.querySelector('antrea-alert[status="danger"]')?.textContent).toContain('access denied');
    });

    test('disables every control and shows progress while an action is pending', async () => {
        let resolve!: (response: Response) => void;
        stubAPI((_url, init) => init.method === 'POST'
            ? new Promise(r => { resolve = r; }) : json({ items: [bundle] }));
        const root = await mount();
        const download = () => root.querySelector('a[download]')!.getAttribute('aria-disabled');
        expect(download()).toBe('false');
        submit();
        await flush();
        expect(root.querySelector('#collect')?.textContent).toContain('Starting…');
        expect(root.querySelector('#collect')?.hasAttribute('disabled')).toBe(true);
        expect(root.querySelector('#refresh')?.hasAttribute('disabled')).toBe(true);
        expect(root.querySelector('.actions antrea-button')?.hasAttribute('disabled')).toBe(true);
        expect(download()).toBe('true');
        resolve(json(collecting, 202));
        await flush();
        expect(root.querySelector('#collect')?.textContent).toContain('Collect');
        expect(root.querySelector('#collect')?.textContent).not.toContain('Starting…');
    });

    test('shows progress on Refresh while a manual refresh is pending', async () => {
        let resolve!: (response: Response) => void;
        const fetchMock = stubAPI();
        const root = await mount();
        fetchMock.mockImplementation(() => new Promise(r => { resolve = r; }));
        click('#refresh');
        await flush();
        expect(root.querySelector('#refresh')?.textContent).toContain('Refreshing…');
        resolve(json({ items: [bundle] }));
        await flush();
        expect(root.querySelector('#refresh')?.textContent).not.toContain('Refreshing…');
    });

    test('a rejected collection preserves its error while existing bundles keep updating', async () => {
        const fetchMock = stubAPI((_url, init) => init.method === 'POST'
            ? json('access denied', 403) : json({ items: [collecting] }));
        const root = await mount();
        submit();
        await flush();
        await vi.advanceTimersByTimeAsync(2000);
        expect(fetchMock).toHaveBeenCalledTimes(4);
        expect(root.querySelector('antrea-alert[status="danger"]')?.textContent)
            .toContain('access denied');
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

    test('a background poll leaves the controls alone and yields to a user action', async () => {
        let resolvePoll!: (response: Response) => void;
        let polling = false;
        const fetchMock = stubAPI((_url, init) => {
            if (init.method === 'DELETE') return new Response(null, { status: 200 });
            if (!polling) return json({ items: [collecting] });
            return new Promise(r => { resolvePoll = r; });
        });
        vi.spyOn(window, 'confirm').mockReturnValue(true);
        const root = await mount();
        polling = true;
        await vi.advanceTimersByTimeAsync(2000);
        const pollSignal = fetchMock.mock.calls.at(-1)?.[1]?.signal;
        expect(pollSignal?.aborted).toBe(false);
        expect(root.querySelector('#refresh')?.textContent).toContain('Refresh');
        expect(root.querySelector('#refresh')?.textContent).not.toContain('Refreshing');
        expect(root.querySelector('#collect')?.hasAttribute('disabled')).toBe(false);
        expect(root.querySelector('.actions antrea-button')?.hasAttribute('disabled')).toBe(false);
        click('.actions antrea-button');
        await flush();
        expect(pollSignal?.aborted).toBe(true);
        expect(fetchMock.mock.calls.at(-1)?.[1]?.method).toBe('DELETE');
        resolvePoll(json({ items: [collecting] }));
        await flush();
        expect(root.querySelector('[data-bundle-id]')).toBeNull();
    });

    test('drops the collection-started banner once an automatic re-list shows no running collection', async () => {
        let items = [collecting];
        stubAPI((_url, init) => init.method === 'POST' ? json(collecting, 202) : json({ items }));
        const root = await mount();
        submit();
        await flush();
        expect(root.textContent).toContain('Collection started');
        await vi.advanceTimersByTimeAsync(2000);
        expect(root.textContent).toContain('Collection started');
        items = [bundle];
        await vi.advanceTimersByTimeAsync(2000);
        expect(root.textContent).not.toContain('Collection started');
        expect(root.textContent).toContain('Collected');
    });

    test('drops the collection-started banner when a manual refresh shows no running collection', async () => {
        let items = [collecting];
        stubAPI((_url, init) => init.method === 'POST' ? json(collecting, 202) : json({ items }));
        const root = await mount();
        submit();
        await flush();
        expect(root.textContent).toContain('Collection started');
        items = [bundle];
        click('#refresh');
        await flush();
        expect(root.textContent).not.toContain('Collection started');
        expect(root.textContent).toContain('Collected');
    });

    test('a successful action clears an earlier action error', async () => {
        stubAPI((_url, init) => init.method === 'POST' ? json('maximum number of bundles retained', 429)
            : init.method === 'DELETE' ? new Response(null, { status: 200 }) : json({ items: [bundle] }));
        vi.spyOn(window, 'confirm').mockReturnValue(true);
        const root = await mount();
        submit();
        await flush();
        expect(root.querySelector('antrea-alert[status="danger"]')?.textContent)
            .toContain('maximum number of bundles retained');
        click('.actions antrea-button');
        await flush();
        expect(root.textContent).toContain('Bundle deleted for all users.');
        expect(root.querySelector('antrea-alert[status="danger"]')).toBeNull();
    });

    test('returning to the tab keeps a rejected collection error', async () => {
        stubAPI((_url, init) => init.method === 'POST'
            ? json('access denied', 403) : json({ items: [collecting] }));
        const root = await mount();
        submit();
        await flush();
        visibility(true);
        visibility(false);
        await flush();
        expect(root.querySelector('antrea-alert[status="danger"]')?.textContent).toContain('access denied');
    });

    test('a successful background list clears an earlier list error', async () => {
        let fail = true;
        stubAPI(() => fail ? json('boom', 500) : json({ items: [collecting] }));
        const root = await mount();
        expect(root.querySelector('antrea-alert[status="danger"]')).not.toBeNull();
        fail = false;
        visibility(true);
        visibility(false);
        await flush();
        expect(root.querySelector('antrea-alert[status="danger"]')).toBeNull();
    });

    test('keeps expanded sources with their bundle when the list updates', async () => {
        const sources = [{ name: 'p', kind: 'plugin' as const, status: 'Collecting' as const }];
        const first = { ...collecting, id: 'first', sources };
        const second = { ...collecting, id: 'second', sources };
        let items = [first, second];
        stubAPI(() => json({ items }));
        const root = await mount();
        root.querySelector<HTMLDetailsElement>('[data-bundle-id="first"] details')!.open = true;
        items = [second];
        await vi.advanceTimersByTimeAsync(2000);
        expect(root.querySelector('[data-bundle-id="first"]')).toBeNull();
        expect(root.querySelector<HTMLDetailsElement>('[data-bundle-id="second"] details')!.open).toBe(false);
        items = [first, second];
        await vi.advanceTimersByTimeAsync(2000);
        expect(root.querySelector<HTMLDetailsElement>('[data-bundle-id="first"] details')!.open).toBe(false);
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

    test('a mutation preempts a slow poll and is not overlapped by another', async () => {
        let resolve!: (response: Response) => void;
        let lists = 0;
        const fetchMock = stubAPI(() => ++lists === 1 ? json({ items: [collecting] })
            : new Promise(r => { resolve = r; }));
        await mount();
        await vi.advanceTimersByTimeAsync(2000);
        const pollSignal = fetchMock.mock.calls[2][1]?.signal;
        submit();
        await vi.advanceTimersByTimeAsync(10_000);
        expect(pollSignal?.aborted).toBe(true);
        expect(fetchMock).toHaveBeenCalledTimes(4);
        expect(fetchMock.mock.calls[3][1]?.method).toBe('POST');
        resolve(json(collecting, 202));
        await flush();
    });

    test('showing the tab during a pending delete does not start a list that could undo it', async () => {
        let resolveDelete!: (response: Response) => void;
        const fetchMock = stubAPI((_url, init) => init.method === 'DELETE'
            ? new Promise(r => { resolveDelete = r; }) : json({ items: [bundle] }));
        vi.spyOn(window, 'confirm').mockReturnValue(true);
        const root = await mount();
        const lists = () => fetchMock.mock.calls
            .filter(([url, init]) => url === basePath && !init?.method).length;
        expect(lists()).toBe(1);
        click('.actions antrea-button');
        await flush();
        visibility(true);
        visibility(false);
        await flush();
        expect(lists()).toBe(1);
        resolveDelete(new Response(null, { status: 200 }));
        await flush();
        await vi.advanceTimersByTimeAsync(20_000);
        expect(lists()).toBe(1);
        expect(root.querySelector('[data-bundle-id]')).toBeNull();
    });

    test('actions work after reconnecting while a user action was in flight', async () => {
        let stall = true;
        const fetchMock = stubAPI((_url, init) => stall && init.method === 'POST'
            ? new Promise(() => {}) : json({ items: [bundle] }));
        await mount();
        submit();
        await flush();
        el.remove();
        stall = false;
        document.body.appendChild(el);
        await flush();
        const lists = fetchMock.mock.calls.filter(([url, init]) =>
            url === basePath && !init?.method).length;
        expect(lists).toBe(2);
        click('#refresh');
        await flush();
        expect(fetchMock.mock.calls.filter(([url, init]) => url === basePath && !init?.method)).toHaveLength(3);
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
        expect(document.querySelector('a[download]')).toBeNull();
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
        expect(root.textContent).toContain('no longer ready to download');
        expect(root.querySelector('a[download]')).toBeNull();
    });

    test('requires confirmation and explains cancellation before deleting for all users', async () => {
        let items = [collecting];
        const fetchMock = stubAPI((_url, init) => init.method === 'DELETE'
            ? new Response(null, { status: 200 }) : json({ items }));
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
        items = [];
        click('#refresh');
        await flush();
        expect(fetchMock).toHaveBeenCalledTimes(4);
        expect(root.textContent).toContain('50 seconds');
    });

    test('keeps the stopping notice when the next collect is rejected while resources are held', async () => {
        stubAPI((_url, init) => init.method === 'DELETE' ? new Response(null, { status: 200 })
            : init.method === 'POST' ? json('at most 2 bundles may be collected at once', 429) : json({ items: [collecting] }));
        vi.spyOn(window, 'confirm').mockReturnValue(true);
        const root = await mount();
        click('.actions antrea-button');
        await flush();
        expect(root.textContent).toContain('Collection is stopping');
        submit();
        await flush();
        expect(root.querySelector('antrea-alert[status="danger"]')?.textContent).toContain('at most 2 bundles');
        expect(root.textContent).toContain('Collection is stopping');
    });

    test('a rejected delete drops the collection-started banner', async () => {
        stubAPI((_url, init) => init.method === 'POST' ? json(collecting, 202)
            : init.method === 'DELETE' ? json('access denied', 403) : json({ items: [bundle] }));
        vi.spyOn(window, 'confirm').mockReturnValue(true);
        const root = await mount();
        submit();
        await flush();
        expect(root.textContent).toContain('Collection started');
        click('.actions antrea-button');
        await flush();
        expect(root.textContent).not.toContain('Collection started');
        expect(root.querySelector('antrea-alert[status="danger"]')?.textContent).toContain('access denied');
    });

    test('ignores a delete while another action is pending', async () => {
        let resolve!: (response: Response) => void;
        const fetchMock = stubAPI((_url, init) => init.method === 'POST'
            ? new Promise(r => { resolve = r; }) : json({ items: [bundle] }));
        const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
        const root = await mount();
        submit();
        await flush();
        root.querySelector('.actions antrea-button')!.dispatchEvent(new MouseEvent('click'));
        await flush();
        expect(confirm).not.toHaveBeenCalled();
        expect(fetchMock.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false);
        resolve(json(collecting, 202));
        await flush();
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
