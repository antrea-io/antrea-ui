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

import React from 'react';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { mockIntersectionObserver } from 'jsdom-testing-mocks';
import Settings from './settings';
import { accountAPI } from '../api/account';
import { APIError } from '../api/common';
import { Settings as AppSettings } from '../api/settings';
import AppSettingsContext from '../components/settings';
import { Provider } from 'react-redux';
import { setupStore, AppStore } from '../store';

// required by Clarity
mockIntersectionObserver();

jest.mock('../api/account');

const mockLogout = jest.fn();
jest.mock('../components/logout', () => ({
    useLogout: () => mockLogout,
}));

const mockAddError = jest.fn();
jest.mock('../components/errors', () => ({
    useAppError: () => {
        const addError = mockAddError;
        return { addError };
    }
}));

const defaultSettings = {
    version: 'v0.1.0',
    auth: {
        basicEnabled: true,
        oidcEnabled: false,
    },
} as AppSettings;

interface testInputs {
    currentPassword?: string
    newPassword?: string
    newPassword2?: string
}

async function inputsToEvents(inputs: testInputs) {
    if (inputs.currentPassword) userEvent.type(await screen.findByLabelText('Current Password'), inputs.currentPassword);
    if (inputs.newPassword) userEvent.type(await screen.findByLabelText('New Password'), inputs.newPassword);
    if (inputs.newPassword2) userEvent.type(await screen.findByLabelText('Confirm New Password'), inputs.newPassword2);
}

describe('Settings', () => {
    const mockedAccountAPI = jest.mocked(accountAPI, true);
    jest.spyOn(console, 'error').mockImplementation(() => {});
    let store: AppStore;

    afterAll(() => {
        jest.restoreAllMocks();
    });
    beforeEach(() => {
        store = setupStore();
    });
    afterEach(() => {
        jest.resetAllMocks();
    });

    const TestProviders = (props: React.PropsWithChildren<{ settings: AppSettings }>) => {
        return (
            <MemoryRouter>
                <Provider store={store}>
                    <AppSettingsContext.Provider value={props.settings}>
                        { props.children }
                    </AppSettingsContext.Provider>
                </Provider>
            </MemoryRouter>
        );
    };

    const badPassword = 'pswdBad';
    const currentPassword = 'pswd1';
    const newPassword = 'pswd2';

    describe('Update Password', () => {
        test('update is successful', async () => {
            mockedAccountAPI.updatePassword.mockResolvedValueOnce();
            render(<TestProviders settings={defaultSettings}><Settings /></TestProviders>);
            await inputsToEvents({currentPassword: currentPassword, newPassword: newPassword, newPassword2: newPassword});
            // unclear why this is needed, but without it the form is not submitted
            await userEvent.click(document.body);
            userEvent.click(screen.getByRole('button', {name: 'Submit'}));
            await waitFor(() => expect(mockLogout).toHaveBeenCalledWith('Your password was successfully updated, please login again'));
            expect(mockedAccountAPI.updatePassword).toHaveBeenCalledWith(currentPassword, newPassword);
            expect(mockAddError).not.toHaveBeenCalled();
        });

        test('update failed', async () => {
            const err = new APIError(400, 'Bad Request', 'Invalid password');
            mockedAccountAPI.updatePassword.mockRejectedValueOnce(err);
            render(<TestProviders settings={defaultSettings}><Settings /></TestProviders>);
            await inputsToEvents({currentPassword: badPassword, newPassword: newPassword, newPassword2: newPassword});
            await userEvent.click(document.body);
            userEvent.click(screen.getByRole('button', {name: 'Submit'}));
            await waitFor(() => expect(mockAddError).toHaveBeenCalledWith(err));
            expect(mockedAccountAPI.updatePassword).toHaveBeenCalledWith(badPassword, newPassword);
            expect(mockLogout).not.toHaveBeenCalled();
        });

        describe('Invalid form', () => {
            interface testCase {
                name: string
                inputs: testInputs
                expectedError: string
            }

            const testCases: testCase[] = [
                {
                    name: 'missing current password',
                    inputs: {
                        newPassword: newPassword,
                        newPassword2: newPassword,
                    },
                    expectedError: 'Required field',
                },
                {
                    name: 'missing new password',
                    inputs: {
                        currentPassword: currentPassword,
                    },
                    expectedError: 'Required field',
                },
                {
                    name: 'missing new password confirmation',
                    inputs: {
                        currentPassword: currentPassword,
                        newPassword: newPassword,
                    },
                    expectedError: 'Required field',
                },
                {
                    name: 'new passwords do not match',
                    inputs: {
                        currentPassword: currentPassword,
                        newPassword: newPassword,
                        newPassword2: currentPassword,
                    },
                    expectedError: "Passwords don't match",
                },
            ];

            test.each<testCase>(testCases)('$name', async (tc: testCase) => {
                render(<TestProviders settings={defaultSettings}><Settings /></TestProviders>);
                await inputsToEvents(tc.inputs);
                await userEvent.click(document.body);
                userEvent.click(screen.getByRole('button', {name: 'Submit'}));
                expect(await screen.findAllByText(tc.expectedError)).not.toHaveLength(0);
                expect(mockedAccountAPI.updatePassword).not.toHaveBeenCalled();
            });
        });
    });
});
