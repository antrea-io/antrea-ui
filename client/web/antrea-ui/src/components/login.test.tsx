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

import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { mockIntersectionObserver } from 'jsdom-testing-mocks';
import Login, { LoginBasic, LoginOIDC } from './login';
import { Settings } from '../api/settings';
import { authAPI, Token } from '../api/auth';
import { APIError } from '../api/common';

// required by Clarity
mockIntersectionObserver();

jest.mock('../api/auth');

const mockedAuthAPI = jest.mocked(authAPI, true);
const consoleErrorMock = jest.spyOn(console, 'error');

const mockAddError = jest.fn();
jest.mock('../components/errors', () => ({
    useAppError: () => {
        const addError = mockAddError;
        return { addError };
    }
}));

let token: string | undefined;

const setToken = (t: string) => {
    token = t;
};

beforeEach(() => {
    consoleErrorMock.mockImplementation();
});

afterAll(() => {
    jest.restoreAllMocks();
});
afterEach(() => {
    token = undefined;
    jest.resetAllMocks();
});

describe('Login', () => {
    test('no auth', () => {
        const settings = {
            auth: {
                basicEnabled: false,
                oidcEnabled: false,
            },
        } as Settings;

        render(<Login setToken={setToken} settings={settings} />);

        expect(screen.queryByText('Login')).toBeNull();
    });

    test('basic only', () => {
        const settings = {
            auth: {
                basicEnabled: true,
                oidcEnabled: false,
            },
        } as Settings;

        render(<Login setToken={setToken} settings={settings} />);

        expect(screen.getByRole('button', { name: 'Login' })).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: 'Login with OIDC' })).toBeNull();
    });

    test('oidc only', () => {
        const settings = {
            auth: {
                basicEnabled: false,
                oidcEnabled: true,
            },
        } as Settings;

        render(<Login setToken={setToken} settings={settings} />);

        expect(screen.queryByRole('button', { name: 'Login' })).toBeNull();
        expect(screen.getByRole('button', { name: 'Login with OIDC' })).toBeInTheDocument();
    });

    test('both', () => {
        const settings = {
            auth: {
                basicEnabled: true,
                oidcEnabled: true,
            },
        } as Settings;

        render(<Login setToken={setToken} settings={settings} />);

        expect(screen.getByRole('button', { name: 'Login' })).toBeInTheDocument();
        expect(screen.getByRole('button', { name: 'Login with OIDC' })).toBeInTheDocument();
    });

    test('oidc with provider name', () => {
        const settings = {
            auth: {
                basicEnabled: false,
                oidcEnabled: true,
                oidcProviderName: 'Dex',
            },
        } as Settings;

        render(<Login setToken={setToken} settings={settings} />);

        expect(screen.queryByRole('button', { name: 'Login' })).toBeNull();
        expect(screen.getByRole('button', { name: 'Login with Dex' })).toBeInTheDocument();
    });
});

describe('LoginBasic', () => {
    const username = 'admin';
    const password = 'xyz';

    interface testInputs {
        username?: string
        password?: string
    }

    const inputsToEvents = async (inputs: testInputs) => {
        const username = await screen.findByLabelText('Username');
        userEvent.clear(username);
        if (inputs.username) userEvent.type(username, inputs.username);
        if (inputs.password) userEvent.type(await screen.findByLabelText('Password'), password);
    };

    describe('Invalid form', () => {
        interface testCase {
            name: string
            inputs: testInputs
            expectedError: string
        }

        const testCases: testCase[] = [
            {
                name: 'missing username',
                inputs: {
                    password: password,
                },
                expectedError: 'Required field',
            },
            {
                name: 'missing password',
                inputs: {
                    username: username,
                },
                expectedError: 'Required field',
            },
        ];

        test.each<testCase>(testCases)('$name', async (tc: testCase) => {
            render(<LoginBasic setToken={setToken} />);
            await inputsToEvents(tc.inputs);
            await userEvent.click(document.body);
            userEvent.click(screen.getByRole('button', {name: 'Login'}));
            expect(await screen.findAllByText(tc.expectedError)).not.toHaveLength(0);
            expect(mockedAuthAPI.login).not.toHaveBeenCalled();
        });
    });

    test('login successful', async () => {
        mockedAuthAPI.login.mockResolvedValueOnce({ accessToken: 'my token' } as Token);
        render(<LoginBasic setToken={setToken} />);
        await inputsToEvents({ username: username, password: password } as testInputs);
        await userEvent.click(document.body);
        userEvent.click(screen.getByRole('button', {name: 'Login'}));
        await waitFor(() => expect(mockedAuthAPI.login).toHaveBeenCalled());
        expect(token).toEqual('my token');
        expect(mockAddError).not.toHaveBeenCalled();
    });

    test('login failed', async () => {
        const err = new APIError(401, 'Unauthorized', 'invalid password');
        mockedAuthAPI.login.mockRejectedValueOnce(err);
        render(<LoginBasic setToken={setToken} />);
        await inputsToEvents({ username: username, password: password } as testInputs);
        await userEvent.click(document.body);
        userEvent.click(screen.getByRole('button', {name: 'Login'}));
        await waitFor(() => expect(mockedAuthAPI.login).toHaveBeenCalled());
        await waitFor(() => expect(mockAddError).toHaveBeenCalledWith(err));
        expect(token).not.toBeDefined();
    });
});

describe('LoginOIDC', () => {
    const providerName = 'Dex';

    const getHrefMock = jest.fn();
    const setHrefMock = jest.fn();
    const oldLocation = Object.getOwnPropertyDescriptor(window, 'location');

    beforeAll(() => {
        const newLocation = {};
        Object.defineProperty(newLocation, 'href', {
            get: getHrefMock,
            set: setHrefMock,
        });
        Object.defineProperty(window, 'location', {
            value: newLocation,
        });
    });

    afterAll(() => {
        Object.defineProperty(window, 'location', oldLocation!);
    });

    afterEach(() => {
        localStorage.removeItem('ui.antrea.io/use-oidc');
    });

    test('first login', () => {
        render(<LoginOIDC providerName={providerName} />);
        expect(screen.getByRole('button', { name: `Login with ${providerName}` })).toBeInTheDocument();
        expect(getHrefMock).not.toHaveBeenCalled();
        expect(setHrefMock).not.toHaveBeenCalled();
    });

    test('auto login', () => {
        getHrefMock.mockImplementation(() => 'http://localhost:3000/summary');
        setHrefMock.mockImplementation((x) => {});
        localStorage.setItem('ui.antrea.io/use-oidc', 'yes');
        render(<LoginOIDC providerName={providerName} />);
        expect(getHrefMock).toHaveBeenCalledTimes(1);
        const params = new URLSearchParams();
        params.set('redirect_url', 'http://localhost:3000/summary');
        expect(setHrefMock).toHaveBeenCalledWith(`/auth/oauth2/login?${params.toString()}`);
    });
});
