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

// The app never holds a credential: authentication is an HttpOnly session cookie that the
// browser attaches on its own. All the app needs to track is whether there is a session.
//   'unknown'       — we haven't probed GET /auth/session yet
//   'authenticated' — the probe (or a login) succeeded
//   'anonymous'     — the probe returned 401, or the user logged out
export type SessionState = 'unknown' | 'authenticated' | 'anonymous';

interface state {
    session: SessionState
}

const initialState = {
    session: 'unknown',
} as state;

const authSlice = createSlice({
    name: 'auth',
    initialState: initialState,
    reducers: {
        setSession(state, action: PayloadAction<SessionState>) {
            state.session = action.payload;
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

export const { setSession } = authSlice.actions;

export type RootState = ReturnType<typeof authSlice.reducer>
export type AppStore = ReturnType<typeof setupStore>
export type AppDispatch = typeof store.dispatch
