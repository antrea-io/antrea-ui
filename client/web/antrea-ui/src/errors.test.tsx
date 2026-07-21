/**
 * Copyright 2026 Antrea Authors.
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

import { useEffect } from 'react';
import { act, render, waitFor } from '@testing-library/react';
import { AppErrorProvider, AppErrorNotification, useAppError } from './errors';

// antrea-alert is a Lit web component with its own shadow DOM; antrea-close is a native
// CustomEvent, not a React synthetic event, so it's dispatched directly rather than via
// fireEvent/userEvent (which only simulate real DOM events, but don't help target the
// custom-element-specific event name here — a plain dispatchEvent is the clearest way to
// exercise exactly what antrea-alert itself fires on close).

function Thrower({ message }: { message: string }) {
    const { addError } = useAppError();
    useEffect(() => { addError(new Error(message)); }, [addError, message]);
    return null;
}

describe('AppErrorNotification', () => {
    test('renders the error message in a danger alert', async () => {
        render(
            <AppErrorProvider>
                <Thrower message="something broke" />
                <AppErrorNotification />
            </AppErrorProvider>,
        );

        await waitFor(() => {
            expect(document.querySelector('antrea-alert[status="danger"]')).not.toBeNull();
        });
        expect(document.querySelector('antrea-alert[status="danger"]')?.textContent).toContain('something broke');
    });

    test('a real antrea-close event clears the error and removes the alert', async () => {
        render(
            <AppErrorProvider>
                <Thrower message="something broke" />
                <AppErrorNotification />
            </AppErrorProvider>,
        );

        const alert = await waitFor(() => {
            const el = document.querySelector('antrea-alert[status="danger"]');
            expect(el).not.toBeNull();
            return el!;
        });
        act(() => {
            alert.dispatchEvent(new CustomEvent('antrea-close', { bubbles: true, composed: true }));
        });

        await waitFor(() => {
            expect(document.querySelector('antrea-alert[status="danger"]')).toBeNull();
        });
    });
});
