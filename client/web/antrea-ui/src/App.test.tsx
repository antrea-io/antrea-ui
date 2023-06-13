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

import React, { ReactElement, useContext } from 'react';
import { act, render, screen, waitFor, RenderOptions } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { Provider } from 'react-redux';
import { mockIntersectionObserver } from 'jsdom-testing-mocks';
import App, { LoginWall, WaitForSettings } from './App';
import { setupStore, AppStore, setToken, store } from './store';
import { authAPI } from './api/auth';
import { APIError } from './api/common';
import { Settings, settingsAPI } from './api/settings';
import SettingsContext from './components/settings';
import { AppErrorProvider, AppErrorNotification } from './components/errors';

// required by Clarity
mockIntersectionObserver();

jest.mock('./api/auth');
jest.mock('./api/settings');

const mockedAuthAPI = jest.mocked(authAPI, true);
const mockedSettingsAPI = jest.mocked(settingsAPI, true);
const consoleErrorMock = jest.spyOn(console, 'error');

let testStore: AppStore;

beforeEach(() => {
    consoleErrorMock.mockImplementation();
});
afterAll(() => {
    jest.restoreAllMocks();
});
afterEach(() => {
    jest.resetAllMocks();
});

const defaultSettings = {
    version: 'v0.1.0',
    auth: {
        basicEnabled: true,
        oidcEnabled: false,
    },
} as Settings;

describe('LoginWall', () => {
    beforeEach(() => {
        testStore = setupStore();
    });

    const TestProviders = (props: React.PropsWithChildren<{ settings: Settings, url: string  }>) => {
        return (
            <MemoryRouter initialEntries={[props.url]}>
                <Provider store={testStore}>
                    <AppErrorProvider>
                        <SettingsContext.Provider value={props.settings}>
                            { props.children }
                        </SettingsContext.Provider>
                        <AppErrorNotification />
                    </AppErrorProvider>
                </Provider>
            </MemoryRouter>
        );
    };

    const customRender = (ui: ReactElement, settings: Settings, url: string = '/', options?: Omit<RenderOptions, 'wrapper'>) => {
        return render(
            <TestProviders settings={settings} url={url}>{ui}</TestProviders>,
            { ...options},
        );
    };

    afterEach(() => {
        localStorage.removeItem('ui.antrea.io/use-oidc');
    });

    test('refresh error - unauthenticated', async () => {
        mockedAuthAPI.refreshToken.mockRejectedValueOnce(new APIError(401, 'Unauthenticated', 'cookie expired'));
        customRender(<LoginWall />, defaultSettings);
        expect(await screen.findByText('Please log in')).toBeInTheDocument();
        expect(mockedAuthAPI.refreshToken).toHaveBeenCalledTimes(1);
        expect(console.error).not.toHaveBeenCalled();
    });

    test('refresh error - other API error', async () => {
        mockedAuthAPI.refreshToken.mockRejectedValueOnce(new APIError(404, 'Not Found'));
        customRender(<LoginWall />, defaultSettings);
        expect(await screen.findByText('Please log in')).toBeInTheDocument();
        expect(await screen.findByText(/Not Found/)).toBeInTheDocument();
        expect(mockedAuthAPI.refreshToken).toHaveBeenCalledTimes(1);
        expect(console.error).toHaveBeenCalled();
    });

    test('refresh error - other error', async () => {
        mockedAuthAPI.refreshToken.mockRejectedValueOnce(new Error('some error'));
        customRender(<LoginWall />, defaultSettings);
        expect(await screen.findByText('Please log in')).toBeInTheDocument();
        expect(await screen.findByText(/some error/)).toBeInTheDocument();
        expect(mockedAuthAPI.refreshToken).toHaveBeenCalledTimes(1);
        expect(console.error).toHaveBeenCalled();
    });

    test('refresh success', () => {
        mockedAuthAPI.refreshToken.mockImplementationOnce(() => {
            testStore.dispatch(setToken('my token'));
            return Promise.resolve();
        });
        customRender(<LoginWall />, defaultSettings);
        expect(screen.queryByText('Please log in')).toBeNull();
        expect(mockedAuthAPI.refreshToken).toHaveBeenCalledTimes(1);
    });

    test('already logged in', () => {
        act(() => {
            testStore.dispatch(setToken('my token'));
        });
        customRender(<LoginWall />, defaultSettings);
        expect(screen.queryByText('Please log in')).toBeNull();
        expect(mockedAuthAPI.refreshToken).not.toHaveBeenCalled();
    });

    test('no log in screen during refresh', async () => {
        mockedAuthAPI.refreshToken.mockImplementation(async () => {
            return new Promise((resolve, reject) => setTimeout(() => reject(new APIError(401, 'Unauthenticated', 'cookie expired')), 200));
        });

        customRender(<LoginWall />, defaultSettings);

        // we wait for refreshToken to be called
        await waitFor(() => expect(mockedAuthAPI.refreshToken).toHaveBeenCalledTimes(1));
        // at this point in time, the log in screen should not be visible (refreshToken has been
        // called but has not completed yet).
        expect(screen.queryByText('Please log in')).toBeNull();
        // the log in screen should eventually be visible (200ms timeout above)
        expect(await screen.findByText('Please log in')).toBeInTheDocument();
    });

    test('oidc search param', async () => {
        const defaultSettings = {
            version: 'v0.1.0',
            auth: {
                basicEnabled: true,
                oidcEnabled: true,
            },
        } as Settings;

        customRender(<LoginWall />, defaultSettings, '/summary?auth_method=oidc');

        await waitFor(() => expect(localStorage.getItem('ui.antrea.io/use-oidc')).toEqual('yes'));
    });
});

describe('WaitForSettings', () => {
    const TestProviders = (props: React.PropsWithChildren) => {
        return (
            <MemoryRouter>
                <AppErrorProvider>
                    { props.children }
                    <AppErrorNotification />
                </AppErrorProvider>
            </MemoryRouter>
        );
    };

    const customRender = (ui: ReactElement, options?: Omit<RenderOptions, 'wrapper'>) => {
        return render(
            <TestProviders>{ui}</TestProviders>,
            { ...options},
        );
    };

    const TestComponent = () => {
        const settings = useContext(SettingsContext);
        return (
            <p>{JSON.stringify(settings)}</p>
        );
    };

    const settings = {
        version: 'v0.1.0',
        auth: {
            basicEnabled: true,
            oidcEnabled: false,
        },
    } as Settings;

    test('success', async () => {
        mockedSettingsAPI.fetch.mockImplementation(async () => {
            // we use a timeout rather than a condition flag
            // using the flag would require some careful use of act() when toggling the flag
            return new Promise((resolve) => setTimeout(() => resolve(settings), 200));
        });

        customRender(<WaitForSettings><TestComponent /></WaitForSettings>);

        await waitFor(() => expect(mockedSettingsAPI.fetch).toHaveBeenCalledTimes(1));
        // while the data is loading, we should see the following message
        expect(screen.getByText('Loading app settings')).toBeInTheDocument();
        expect(await screen.findByText(JSON.stringify(settings))).toBeInTheDocument();
    });

    test('API error', async() => {
        mockedSettingsAPI.fetch.mockRejectedValueOnce(new APIError(404, 'Not Found', 'page not found'));

        customRender(<WaitForSettings><TestComponent /></WaitForSettings>);

        expect(await screen.findByText(/Not Found/)).toBeInTheDocument();
        expect(mockedSettingsAPI.fetch).toHaveBeenCalledTimes(1);
        expect(console.error).toHaveBeenCalled();
    });
});

describe('App', () => {
    beforeEach(() => {
        mockedSettingsAPI.fetch.mockResolvedValue(defaultSettings);
    });

    afterEach(() => {
        act(() => {
            store.dispatch(setToken(undefined));
        });
    });

    test('login', async () => {
        // It's important to use mockImplementationOnce instead of mockImplementation.
        // There is an edge case where calling store.dispatch(setToken(undefined)) in afterEach will
        // cause the component to be updated, with a new call to refreshToken, which in turn will
        // set the token to 'my token', hence impacting subsequent test cases.
        // Unlike for the test suites above, we cannot use an ephemeral test store when testing App.
        mockedAuthAPI.refreshToken.mockImplementationOnce(() => {
            store.dispatch(setToken('my token'));
            return Promise.resolve();
        });
        render(<App />, { wrapper: MemoryRouter });
        await waitFor(() => expect(mockedAuthAPI.refreshToken).toHaveBeenCalledTimes(1));
        expect(screen.queryByText('Please log in')).toBeNull();
    });

    test('logout', async () => {
        mockedAuthAPI.refreshToken.mockImplementationOnce(() => {
            store.dispatch(setToken('my token'));
            return Promise.resolve();
        });
        render(<App />, { wrapper: MemoryRouter });
        // mimic a user login
        await waitFor(() => expect(mockedAuthAPI.refreshToken).toHaveBeenCalledTimes(1));
        expect(screen.queryByText('Please log in')).toBeNull();
        // logout action
        userEvent.click(screen.getByText('Logout'));
        expect(await screen.findByText('Please log in')).toBeInTheDocument();
        expect(store.getState().token).toEqual('');
    });
});
