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
import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { mockIntersectionObserver } from 'jsdom-testing-mocks';
import App from './App';
import { store, setToken } from './store';
import { authAPI } from './api/auth';
import { APIError } from './api/common';

// required by Clarity
mockIntersectionObserver();

jest.mock('./api/auth');

describe('App auth', () => {
    const mockedAuthAPI = jest.mocked(authAPI, true);
    jest.spyOn(console, 'error').mockImplementation(() => {});

    afterAll(() => {
        jest.restoreAllMocks();
    });
    afterEach(() => {
        act(() => {
            store.dispatch(setToken(undefined));
        });
        jest.resetAllMocks();
    });

    test('refresh error - unauthenticated', async () => {
        mockedAuthAPI.refreshToken.mockRejectedValueOnce(new APIError(401, 'Unauthenticated', 'cookie expired'));
        render(<App />, { wrapper: MemoryRouter });
        expect(await screen.findByText('Please log in')).toBeInTheDocument();
        expect(mockedAuthAPI.refreshToken).toHaveBeenCalledTimes(1);
        expect(console.error).not.toHaveBeenCalled();
    });

    test('refresh error - other API error', async () => {
        mockedAuthAPI.refreshToken.mockRejectedValueOnce(new APIError(404, 'Not Found'));
        render(<App />, { wrapper: MemoryRouter });
        expect(await screen.findByText('Please log in')).toBeInTheDocument();
        expect(await screen.findByText(/Not Found/)).toBeInTheDocument();
        expect(mockedAuthAPI.refreshToken).toHaveBeenCalledTimes(1);
        expect(console.error).toHaveBeenCalled();
    });

    test('refresh error - other error', async () => {
        mockedAuthAPI.refreshToken.mockRejectedValueOnce(new Error('some error'));
        render(<App />, { wrapper: MemoryRouter });
        expect(await screen.findByText('Please log in')).toBeInTheDocument();
        expect(await screen.findByText(/some error/)).toBeInTheDocument();
        expect(mockedAuthAPI.refreshToken).toHaveBeenCalledTimes(1);
        expect(console.error).toHaveBeenCalled();
    });

    test('refresh success', () => {
        mockedAuthAPI.refreshToken.mockImplementation(() => {
            store.dispatch(setToken('my token'));
            return Promise.resolve();
        });
        render(<App />, { wrapper: MemoryRouter });
        expect(screen.queryByText('Please log in')).toBeNull();
        expect(mockedAuthAPI.refreshToken).toHaveBeenCalledTimes(1);
    });

    test('already logged in', () => {
        act(() => {
            store.dispatch(setToken('my token'));
        });
        render(<App />, { wrapper: MemoryRouter });
        expect(screen.queryByText('Please log in')).toBeNull();
        expect(mockedAuthAPI.refreshToken).not.toHaveBeenCalled();
    });

    test('logout', async () => {
        act(() => {
            store.dispatch(setToken('my token'));
        });
        render(<App />, { wrapper: MemoryRouter });
        expect(screen.queryByText('Please log in')).toBeNull();
        userEvent.click(screen.getByText('Logout'));
        expect(await screen.findByText('Please log in')).toBeInTheDocument();
        expect(store.getState().token).toEqual('');
    });
});
