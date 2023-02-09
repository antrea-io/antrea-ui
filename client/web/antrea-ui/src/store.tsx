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

export const store = configureStore({
    reducer: authSlice.reducer,
});

export const { setToken } = authSlice.actions;

// Infer the `RootState` and `AppDispatch` types from the store itself
export type RootState = ReturnType<typeof store.getState>
// Inferred type: {posts: PostsState, comments: CommentsState, users: UsersState}
export type AppDispatch = typeof store.dispatch
