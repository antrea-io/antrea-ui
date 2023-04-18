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

import React, { useEffect, useState } from 'react';
import logo from './logo.svg';
import './App.css';
import { Outlet, Link, useLocation } from "react-router-dom";
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
import { APIError } from './api/common';
import { useAppError} from './components/errors';

function LoginWall(props: React.PropsWithChildren) {
    const { state } = useLocation();
    const [msg, setMsg] = useState<string | null>();
    const token = useSelector((state: RootState) => state.token);
    const dispatch = useDispatch();
    const { addError, removeError } = useAppError();

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
            }
        }

        if (token === undefined) {
            // try a refresh
            refreshToken();
        }
    }, [token, addError, removeError]);

    useEffect(() => {
        setMsg(state?.logoutMsg || null);
    }, [state]);

    function doSetToken(token: string) {
        dispatch(setToken(token));
    }

    if (!token) {
        return (
            <div cds-layout="vertical p:md gap:md">
                <p cds-text="section" >Please log in</p>
                <Login setToken={doSetToken} />
                { msg && <>
                    <CdsAlertGroup status="success">
                        <CdsAlert closable onCloseChange={() => setMsg(null)}>{msg}</CdsAlert>
                    </CdsAlertGroup>
                </> }
            </div>
        );
    }

    return (
        <div cds-layout="vertical align:stretch p:md gap:md">
            {props.children}
        </div>
    );
}

function Logout() {
    const [, logout] = useLogout();

    return (
        <CdsButton type="button" action="outline" onClick={() => { logout('You successfully logged out'); }}>Logout</CdsButton>        
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
                        <AppErrorProvider>
                            <div cds-layout="vertical">
                                <LoginWall>
                                    <Outlet />
                                </LoginWall>
                                <AppErrorNotification />
                            </div>
                        </AppErrorProvider>
                    </div>
                </Provider>
            </div>
        </div>
    );
}

export default App;
