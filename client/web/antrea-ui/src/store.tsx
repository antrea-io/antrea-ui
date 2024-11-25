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

import { configureStore, createSlice, PayloadAction } from '@reduxjs/toolkit';

interface state {
    // if token is undefined: we do not have a token in memory but we may have a
    // valid HTTP cookie with a refresh token
    // if token is empty string: we do not have a token in memory and we known
    // that we no longer have a valid HTTP cookie with a refresh token (because
    // we have attempted a refresh)
    // if token is a non-empty string: we have an access token
    token?: string
}

const initialState = {
    token: undefined,
} as state;

const authSlice = createSlice({
    name: 'auth',
    initialState: initialState,
    reducers: {
        setToken(state, action: PayloadAction<string | undefined>) {
            state.token = action.payload;
        }
    }
});

export const setupStore = (preloadedState?: RootState) => {
    return configureStore({
        reducer: authSlice.reducer,
        preloadedState,
    });
};

export const store = setupStore();

export const { setToken } = authSlice.actions;

export type RootState = ReturnType<typeof authSlice.reducer>
export type AppStore = ReturnType<typeof setupStore>
export type AppDispatch = typeof store.dispatch
