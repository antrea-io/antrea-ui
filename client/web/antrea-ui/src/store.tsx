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
import type { SessionInfo } from '@antrea/ui-components';

// The app never holds a credential: authentication is an HttpOnly session cookie that the
// browser attaches on its own. All the app needs to track is whether there is a session.
//   'unknown'       — we haven't probed GET /auth/session yet
//   'authenticated' — the probe (or a login) succeeded
//   'anonymous'     — the probe returned 401, or the user logged out
export type SessionState = 'unknown' | 'authenticated' | 'anonymous';

interface state {
    session: SessionState
    // Populated by setAuthenticated, straight from the login page's own GET /auth/session probe
    // or login call — so nothing else needs a second round-trip just to display who is logged
    // in. null whenever session isn't 'authenticated', and also when setAuthenticated itself
    // couldn't get it (display-only; never blocks authentication).
    sessionInfo: SessionInfo | null
}

const initialState: state = {
    session: 'unknown',
    sessionInfo: null,
};

const authSlice = createSlice({
    name: 'auth',
    initialState: initialState,
    reducers: {
        // 'authenticated' is excluded: setAuthenticated is the only path into that state, so it
        // is always paired with the SessionInfo (or explicit null) that goes with it, rather than
        // leaving sessionInfo stale from whatever setSession('authenticated') last happened to
        // see.
        setSession(state, action: PayloadAction<Exclude<SessionState, 'authenticated'>>) {
            state.session = action.payload;
            state.sessionInfo = null;
        },
        setAuthenticated(state, action: PayloadAction<SessionInfo | null>) {
            state.session = 'authenticated';
            state.sessionInfo = action.payload;
        },
    }
});

// Partial, not RootState: callers (mainly tests) usually only care about overriding one field,
// and merging over initialState here means adding a field never forces every existing caller to
// spell it out.
export const setupStore = (preloadedState?: Partial<RootState>) => {
    return configureStore({
        reducer: authSlice.reducer,
        preloadedState: preloadedState && { ...initialState, ...preloadedState },
    });
};

export const store = setupStore();

export const { setSession, setAuthenticated } = authSlice.actions;

export type RootState = ReturnType<typeof authSlice.reducer>
export type AppStore = ReturnType<typeof setupStore>
export type AppDispatch = typeof store.dispatch
