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

import { useRef, useEffect } from 'react';
import logo from './logo.svg';
import './App.css';
import '@antrea/ui-components';
import { Outlet, Link } from 'react-router';
import NavTab from './nav';
import { useLogout } from './logout';
import { AppErrorProvider, AppErrorNotification } from './errors';
import { Provider, useSelector, useDispatch } from 'react-redux';
import type { RootState } from './store';
import { store, setToken } from './store';

function AuthShell() {
    const token = useSelector((state: RootState) => state.token);
    const dispatch = useDispatch();
    const loginRef = useRef<HTMLElement>(null);

    useEffect(() => {
        const el = loginRef.current;
        if (!el) return;
        const onToken = (e: Event) => {
            dispatch(setToken((e as CustomEvent<{ accessToken: string }>).detail.accessToken));
        };
        el.addEventListener('antrea-token', onToken);
        return () => el.removeEventListener('antrea-token', onToken);
    }, [dispatch]);

    if (!token) {
        return <antrea-login-page ref={loginRef} />;
    }

    return (
        <>
            <NavTab />
            <main className="app-content">
                <Outlet />
            </main>
        </>
    );
}

function Logout() {
    const logout = useLogout();
    return (
        <antrea-button
            type="button"
            action="outline"
            onClick={() => logout('You successfully logged out')}
        >
            Logout
        </antrea-button>
    );
}

function App() {
    return (
        <div className="app-shell">
            <Provider store={store}>
                <header className="app-header">
                    <div className="app-header-left">
                        <Link to="/">
                            <img src={logo} alt="Antrea logo" className="App-logo" />
                        </Link>
                        <h1>Antrea UI</h1>
                    </div>
                    <Logout />
                </header>
                <div className="app-body">
                    <AppErrorProvider>
                        <AuthShell />
                        <AppErrorNotification />
                    </AppErrorProvider>
                </div>
            </Provider>
        </div>
    );
}

export default App;
