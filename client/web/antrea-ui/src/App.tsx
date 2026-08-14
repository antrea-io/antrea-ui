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

import { useRef, useEffect, useState } from 'react';
import logo from './logo.svg';
import './App.css';
import '@antrea/ui-components';
import { apiSession, APIError, resetAccessSummary, displaySessionMode, displaySessionUsername } from '@antrea/ui-components';
import type { SessionInfo } from '@antrea/ui-components';
import { Outlet, Link } from 'react-router';
import NavTab from './nav';
import { useLogout } from './logout';
import { AppErrorProvider, AppErrorNotification } from './errors';
import { AccessProvider } from './access';
import { Provider, useSelector, useDispatch } from 'react-redux';
import type { RootState } from './store';
import { store, setSession } from './store';
import type { PluginSidebarEntry } from './plugins';

// How often to ping GET /auth/session while a tab is visible. The backend expires a session
// after 30 minutes without a request, and "idle" should mean "no open visible tab" rather than
// "no clicks" — otherwise a tab left open over lunch logs the user out on their next click,
// which is a re-login prompt for the admin password and a re-upload for kubeconfig/token logins.
// The trade-off is that an unattended but visible tab holds its session for the full 12h cap.
//
// One exception, enforced on the backend rather than here: an attached flow stream keeps its
// session alive whether or not the tab is visible, because a flow-visibility tab is something
// people background on purpose. See RequestAuth.KeepAlive in pkg/auth/session/context.go.
const SESSION_KEEPALIVE_MS = 5 * 60 * 1000;

/** Keeps the session alive while this tab is visible. One app-level timer, not one per page. */
function useSessionKeepalive(enabled: boolean) {
    const dispatch = useDispatch();
    useEffect(() => {
        if (!enabled) return;
        const ping = () => {
            if (document.visibilityState !== 'visible') return;
            apiSession().catch(err => {
                // Only a 401 means the session is actually gone. Anything else (a network blip,
                // a 5xx, a backend rolling restart) is transient — do not log a working session
                // out over it; the next keepalive tick will try again.
                if (!(err instanceof APIError && err.code === 401)) return;
                // Flip to anonymous so the login page renders; the user still has to go through
                // /auth/logout to clear any provider-side state, which the Logout button does.
                dispatch(setSession('anonymous'));
            });
        };
        const timer = setInterval(ping, SESSION_KEEPALIVE_MS);
        return () => clearInterval(timer);
    }, [enabled, dispatch]);
}

function AuthShell({ pluginSidebarEntries }: { pluginSidebarEntries: PluginSidebarEntry[] }) {
    const session = useSelector((state: RootState) => state.session);
    const dispatch = useDispatch();
    const loginRef = useRef<HTMLElement>(null);

    useSessionKeepalive(session === 'authenticated');

    useEffect(() => {
        const el = loginRef.current;
        if (!el) return;
        // The login page probes GET /auth/session on mount and logs in on submit; either way it
        // tells us when there is a session. There is no token in the event: the credential
        // lives server-side, keyed by an HttpOnly cookie.
        const onAuthenticated = () => {
            // A fresh login: any access-summary fetched (or attempted) for a previous session
            // must not leak into this one.
            resetAccessSummary();
            dispatch(setSession('authenticated'));
        };
        el.addEventListener('antrea-authenticated', onAuthenticated);
        return () => el.removeEventListener('antrea-authenticated', onAuthenticated);
    }, [dispatch]);

    if (session !== 'authenticated') {
        return <antrea-login-page ref={loginRef} />;
    }

    return (
        <>
            <NavTab pluginSidebarEntries={pluginSidebarEntries} />
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

// Shows the logged-in username and login mode, straight from GET /auth/session — the same
// source the keepalive ping and the settings page use. Renders nothing until that resolves, and
// nothing at all if it fails, since the rest of the shell already fails open on that same
// condition.
function UserIdentity() {
    const session = useSelector((state: RootState) => state.session);
    const [info, setInfo] = useState<SessionInfo | null>(null);

    // Same pattern as AccessProvider (access.tsx): drop stale info during render rather than in
    // an effect, so a session change never paints a previous identity for one frame.
    const [sessionForInfo, setSessionForInfo] = useState(session);
    if (sessionForInfo !== session) {
        setSessionForInfo(session);
        setInfo(null);
    }

    useEffect(() => {
        if (session !== 'authenticated') return;
        let cancelled = false;
        apiSession()
            .then((i) => { if (!cancelled) setInfo(i); })
            .catch(() => { if (!cancelled) setInfo(null); });
        return () => { cancelled = true; };
    }, [session]);

    if (!info?.username || !info.mode) return null;
    return (
        <div className="app-user-identity">
            <span className="app-user-identity-name">{displaySessionUsername(info.mode, info.username)}</span>
            <span className="app-user-identity-role">{displaySessionMode(info.mode)}</span>
        </div>
    );
}

function App({ pluginSidebarEntries = [] }: { pluginSidebarEntries?: PluginSidebarEntry[] }) {
    return (
        <div className="app-shell">
            <Provider store={store}>
                <AppErrorProvider>
                    <AccessProvider>
                        <header className="app-header">
                            <div className="app-header-left">
                                <Link to="/">
                                    <img src={logo} alt="Antrea logo" className="App-logo" />
                                </Link>
                                <h1>Antrea UI</h1>
                            </div>
                            <div className="app-header-right">
                                <UserIdentity />
                                <Logout />
                            </div>
                        </header>
                        <div className="app-body">
                            <AuthShell pluginSidebarEntries={pluginSidebarEntries} />
                            <AppErrorNotification />
                        </div>
                    </AccessProvider>
                </AppErrorProvider>
            </Provider>
        </div>
    );
}

export default App;
