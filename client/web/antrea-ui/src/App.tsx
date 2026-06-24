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

import React, { useEffect, useState, useContext } from 'react';
import logo from './logo.svg';
import './App.css';
import '@antrea/ui-components';
import { Outlet, Link, useSearchParams } from 'react-router';
import NavTab from './components/nav';
import Login from './components/login';
import { useLogout } from './components/logout';
import { AppErrorProvider, AppErrorNotification } from './components/errors';
import { Provider, useSelector, useDispatch } from 'react-redux';
import type { RootState } from './store';
import { store, setToken } from './store';
import { authAPI } from './api/auth';
import { Settings, settingsAPI } from './api/settings';
import { APIError } from './api/common';
import { useAppError } from './components/errors';
import { WaitForAPIResource } from './components/progress';
import SettingsContext from './components/settings';

export function LoginWall(props: React.PropsWithChildren) {
    const settings = useContext(SettingsContext);
    const [msg, setMsg] = useState<string | null>();
    const token = useSelector((state: RootState) => state.token);
    const dispatch = useDispatch();
    const { addError, removeError } = useAppError();
    const [refreshDone, setRefreshDone] = useState<boolean>(false);
    const [searchParams, setSearchParams] = useSearchParams();

    useEffect(() => {
        removeError();

        async function refreshToken() {
            try {
                await authAPI.refreshToken();
            } catch (e) {
                if (e instanceof APIError && e.code === 401) {
                    return;
                }
                if (e instanceof Error) addError(e);
                console.error(e);
            } finally {
                setRefreshDone(true);
            }
        }

        if (token === undefined) {
            refreshToken();
        } else {
            setRefreshDone(true);
        }
    }, [token, addError, removeError]);

    useEffect(() => {
        setMsg(searchParams.get('msg'));
    }, [searchParams, setSearchParams]);

    useEffect(() => {
        const authMethod = searchParams.get('auth_method');
        if (authMethod === 'oidc' && settings.auth.oidcEnabled) {
            localStorage.setItem('ui.antrea.io/use-oidc', 'yes');
        }
        if (authMethod) {
            searchParams.delete('auth_method');
            setSearchParams(searchParams);
        }
    }, [searchParams, setSearchParams, settings]);

    function doSetToken(token: string) {
        dispatch(setToken(token));
    }

    if (token) {
        return <>{props.children}</>;
    }

    return (
        <WaitForAPIResource ready={refreshDone} text="Attempting to authenticate">
            <div className="login-wall">
                <h2>Please log in</h2>
                <Login setToken={doSetToken} settings={settings} />
                {msg && (
                    <antrea-alert
                        status="success"
                        closable
                        onAntreaClose={() => setMsg(null)}
                    >
                        {msg}
                    </antrea-alert>
                )}
            </div>
        </WaitForAPIResource>
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

export function WaitForSettings(props: React.PropsWithChildren) {
    const [settings, setSettings] = useState<Settings>();
    const { addError, removeError } = useAppError();

    useEffect(() => {
        async function getSettings() {
            try {
                const s = await settingsAPI.fetch();
                setSettings(s);
                removeError();
            } catch (e) {
                if (e instanceof Error) addError(e);
                console.error(e);
            }
        }

        getSettings();
    }, [addError, removeError]);

    return (
        <WaitForAPIResource ready={settings !== undefined} text="Loading app settings">
            <SettingsContext.Provider value={settings!}>
                {props.children}
            </SettingsContext.Provider>
        </WaitForAPIResource>
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
                    <NavTab />
                    <main className="app-content">
                        <AppErrorProvider>
                            <WaitForSettings>
                                <LoginWall>
                                    <Outlet />
                                </LoginWall>
                            </WaitForSettings>
                            <AppErrorNotification />
                        </AppErrorProvider>
                    </main>
                </div>
            </Provider>
        </div>
    );
}

export default App;
