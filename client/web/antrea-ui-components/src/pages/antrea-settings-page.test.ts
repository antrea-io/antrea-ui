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

import { afterEach, describe, expect, test, vi } from 'vitest';
import './antrea-settings-page';
import type { AntreaSettingsPage } from './antrea-settings-page';

let el: AntreaSettingsPage | undefined;

afterEach(() => {
    el?.remove();
    el = undefined;
    vi.unstubAllGlobals();
});

async function mount(): Promise<AntreaSettingsPage> {
    el = document.createElement('antrea-settings-page') as AntreaSettingsPage;
    el.token = 'my-token';
    document.body.appendChild(el);
    await el.updateComplete;
    return el;
}

function fillAndSubmit(
    page: AntreaSettingsPage,
    inputs: { current?: string; next?: string; confirm?: string },
) {
    const root = page.shadowRoot!;
    if (inputs.current !== undefined) {
        (root.querySelector('#current-password') as HTMLInputElement & { value: string }).value = inputs.current;
    }
    if (inputs.next !== undefined) {
        (root.querySelector('#new-password') as HTMLInputElement & { value: string }).value = inputs.next;
    }
    if (inputs.confirm !== undefined) {
        (root.querySelector('#confirm-password') as HTMLInputElement & { value: string }).value = inputs.confirm;
    }
    root.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
}

// antrea-input renders its error message inside its own shadow root, so a plain
// page.shadowRoot!.textContent check (which doesn't cross that boundary) won't see it.
function fieldErrorText(page: AntreaSettingsPage, id: string): string {
    return page.shadowRoot!.querySelector(`#${id}`)!.shadowRoot!.textContent ?? '';
}

async function flush(page: AntreaSettingsPage) {
    await page.updateComplete;
    await new Promise(r => setTimeout(r, 0));
    await page.updateComplete;
}

describe('AntreaSettingsPage — validation', () => {
    test.each([
        {
            name: 'missing current password',
            inputs: { next: 'newpassword1', confirm: 'newpassword1' },
            fieldId: 'current-password',
            expectedError: 'Current password is required',
        },
        {
            name: 'missing new password',
            inputs: { current: 'oldpassword1' },
            fieldId: 'new-password',
            expectedError: 'New password is required',
        },
        {
            name: 'new password too short',
            inputs: { current: 'oldpassword1', next: 'short', confirm: 'short' },
            fieldId: 'new-password',
            expectedError: 'Password must be at least 8 characters',
        },
        {
            name: 'new passwords do not match',
            inputs: { current: 'oldpassword1', next: 'newpassword1', confirm: 'newpassword2' },
            fieldId: 'confirm-password',
            expectedError: 'Passwords do not match',
        },
    ])('$name', async ({ inputs, fieldId, expectedError }) => {
        const fetchMock = vi.fn();
        vi.stubGlobal('fetch', fetchMock);
        const page = await mount();

        fillAndSubmit(page, inputs);
        await flush(page);

        expect(fieldErrorText(page, fieldId)).toContain(expectedError);
        expect(fetchMock).not.toHaveBeenCalled();
    });
});

describe('AntreaSettingsPage — submit', () => {
    test('successful update shows a success banner, sends base64-encoded passwords, and clears the form', async () => {
        const fetchMock = vi.fn().mockResolvedValue(new Response('', { status: 200 }));
        vi.stubGlobal('fetch', fetchMock);
        const page = await mount();

        fillAndSubmit(page, { current: 'oldpassword1', next: 'newpassword1', confirm: 'newpassword1' });
        await flush(page);

        expect(fetchMock).toHaveBeenCalledTimes(1);
        const [url, init] = fetchMock.mock.calls[0];
        expect(url).toBe('/api/v1/account/password');
        expect(init.method).toBe('PUT');
        const body = JSON.parse(init.body as string);
        expect(body).toEqual({
            currentPassword: btoa('oldpassword1'),
            newPassword: btoa('newpassword1'),
        });

        expect(page.shadowRoot!.querySelector('antrea-alert[status="success"]')?.textContent)
            .toContain('Password changed successfully');
        expect(page.shadowRoot!.querySelector<HTMLInputElement>('#current-password')!.value).toBe('');
        expect(page.shadowRoot!.querySelector<HTMLInputElement>('#new-password')!.value).toBe('');
    });

    test('failed update shows an error banner and does not clear the form', async () => {
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('invalid current password', {
            status: 400,
            statusText: 'Bad Request',
        })));
        const page = await mount();

        fillAndSubmit(page, { current: 'wrongpassword', next: 'newpassword1', confirm: 'newpassword1' });
        await flush(page);

        expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')?.textContent)
            .toContain('invalid current password');
        expect(page.shadowRoot!.querySelector('antrea-alert[status="success"]')).toBeNull();
    });

    test('a 401 response dispatches antrea-session-expired instead of showing an error banner', async () => {
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('', { status: 401 })));
        const page = await mount();
        const onSessionExpired = vi.fn();
        page.addEventListener('antrea-session-expired', onSessionExpired);

        fillAndSubmit(page, { current: 'oldpassword1', next: 'newpassword1', confirm: 'newpassword1' });
        await flush(page);

        expect(onSessionExpired).toHaveBeenCalledTimes(1);
        expect(page.shadowRoot!.querySelector('antrea-alert[status="danger"]')).toBeNull();
    });
});
