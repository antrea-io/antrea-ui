import { store, setToken as writeToken } from '../store';

export function getToken(): string {
    return store.getState().token || "";
}

export function setToken(token: string) {
    store.dispatch(writeToken(token));
}
