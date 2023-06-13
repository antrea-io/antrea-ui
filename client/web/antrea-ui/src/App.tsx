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
import { Outlet, Link, useSearchParams } from "react-router-dom";
import NavTab from './components/nav';
import Login from './components/login';
import { useLogout } from './components/logout';
import { CdsButton } from '@cds/react/button';
import { CdsAlertGroup, CdsAlert } from "@cds/react/alert";
import { AppErrorProvider, AppErrorNotification } from './components/errors';
import { Provider, useSelector, useDispatch } from 'react-redux';
import type { RootState } from './store';
import { store, setToken } from './store';
import { authAPI } from './api/auth';
import { Settings, settingsAPI } from './api/settings';
import { APIError } from './api/common';
import { useAppError} from './components/errors';
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
                    // ignore 401 errors
                    return;
                }
                if (e instanceof Error) addError(e);
                console.error(e);
            } finally {
                // indicate that the refresh API call has completed
                setRefreshDone(true);
            }
        }

        if (token === undefined) {
            // try a refresh
            refreshToken();
        } else {
            // If token is defined and we don't need to call refreshToken, set the refreshDone flag
            // to true automatically.
            // Not sure how important this is in practice, except maybe for unit tests.
            // For users, the only way to have a defined token is if refreshToken has actually been
            // called. In unit tests, it could be possible to set the token directly without having
            // an actual call to refreshToken.
            setRefreshDone(true);
        }
    }, [token, addError, removeError]);

    useEffect(() => {
        // From the React documentation:
        // > By default, React DOM escapes any values embedded in JSX before rendering them.
        // So we should not have to worry about displaying msg as is.
        setMsg(searchParams.get('msg'));
    }, [searchParams, setSearchParams]);

    useEffect(() => {
        // we use this to remember that we successfully authenticated with OIDC
        // when the refreshToken expires, we will use this information to login with OIDC by default
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
        return (
            <div cds-layout="vertical align:stretch">
                {props.children}
            </div>
        );
    }

    return (
        <WaitForAPIResource ready={refreshDone} text="Attempting to authenticate">
            <div cds-layout="vertical">
                <p cds-text="section" >Please log in</p>
                <Login setToken={doSetToken} settings={settings} />
                { msg && <>
                    <CdsAlertGroup status="success">
                        <CdsAlert closable onCloseChange={() => setMsg(null)}>{msg}</CdsAlert>
                    </CdsAlertGroup>
                </> }
            </div>
        </WaitForAPIResource>
    );
}

function Logout() {
    const logout = useLogout();

    return (
        <CdsButton type="button" action="outline" onClick={() => { logout('You successfully logged out'); }}>Logout</CdsButton>        
    );
}

export function WaitForSettings(props: React.PropsWithChildren) {
    const [settings, setSettings] = useState<Settings>();
    const { addError, removeError } = useAppError();

    useEffect(() => {
        async function getSettings() {
            try {
                const settings = await settingsAPI.fetch();
                setSettings(settings);
                removeError();
            } catch (e) {
                if (e instanceof Error) addError(e);
                console.error(e);
            }
        }

        getSettings();
    }, [addError, removeError, setSettings]);

    return (
        <WaitForAPIResource ready={settings !== undefined} text='Loading app settings'>
            <SettingsContext.Provider value={settings!}>
                {props.children}
            </SettingsContext.Provider>
        </WaitForAPIResource>
    );
}

function App() {
    return (
        <div cds-text="body" cds-theme="dark">
            {/* 100vh to fill the whole screen */}
            <div style={{ height: "fit-content", minHeight: "100vh" }} cds-layout="vertical gap:md align:top">
                <Provider store={store}>
                    <header cds-layout="horizontal wrap:none gap:md m-t:lg" style={{ display: "flex", alignItems: "center", justifyContent: "space-between", padding:"0px 12px"}}>
                        <div style={{ display: "flex", alignItems: "center", gap: "10px" }}>
                            <Link to="/">
                                <img src={logo} alt="logo" style={{ height: "2rem" }} />
                            </Link>
                            <p cds-text="heading" cds-layout="align:vertical-center">Antrea UI</p>
                        </div>
                        <Logout />
                    </header>
                    <div cds-layout="horizontal align:top wrap:none" style={{ height: "100%" }}>
                        <NavTab />
                        <div cds-layout="vertical p:md gap:md">
                            <AppErrorProvider>
                                <WaitForSettings>
                                    <LoginWall>
                                        <Outlet />
                                    </LoginWall>
                                </WaitForSettings>
                                <AppErrorNotification />
                            </AppErrorProvider>
                        </div>
                    </div>
                </Provider>
            </div>
        </div>
    );
}

export default App;
