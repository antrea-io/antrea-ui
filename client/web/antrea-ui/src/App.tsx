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

import { useEffect, useState } from 'react';
import logo from './logo.svg';
import './App.css';
import '@antrea/ui-components';
import { apiSession, APIError, resetAccessSummary, sessionIdentity } from '@antrea/ui-components';
import type { SessionInfo } from '@antrea/ui-components';
import { Outlet, Link } from 'react-router';
import NavTab from './nav';
import { useLogout } from './logout';
import { AppErrorProvider, AppErrorNotification } from './errors';
import { AccessProvider } from './access';
import { Provider, useSelector, useDispatch } from 'react-redux';
import type { RootState } from './store';
import { store, setSession, setAuthenticated } from './store';
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
    // A callback ref, not useRef: a keepalive 401 flips session back to 'anonymous' while
    // AuthShell stays mounted, so React swaps in a brand new <antrea-login-page> element. A
    // plain ref wouldn't tell the effect below that its target changed, so it would go on
    // listening to the unmounted one and miss the re-login entirely.
    const [loginEl, setLoginEl] = useState<HTMLElement | null>(null);

    useSessionKeepalive(session === 'authenticated');

    useEffect(() => {
        if (!loginEl) return;
        // The login page probes GET /auth/session on mount and logs in on submit; either way it
        // tells us when there is a session, and hands over the SessionInfo it already fetched
        // (or fetched to confirm the login) as the event detail — no token in it, the credential
        // lives server-side, keyed by an HttpOnly cookie. detail is null when that fetch failed;
        // the login itself still succeeded, there is just nothing to display for it.
        const onAuthenticated = (e: Event) => {
            // A fresh login: any access-summary fetched (or attempted) for a previous session
            // must not leak into this one.
            resetAccessSummary();
            dispatch(setAuthenticated((e as CustomEvent<SessionInfo>).detail ?? null));
        };
        loginEl.addEventListener('antrea-authenticated', onAuthenticated);
        return () => loginEl.removeEventListener('antrea-authenticated', onAuthenticated);
    }, [loginEl, dispatch]);

    if (session !== 'authenticated') {
        return <antrea-login-page ref={setLoginEl} />;
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

// Shows the logged-in username and account kind, from the SessionInfo the login page already
// fetched (see the antrea-authenticated listener above) — no fetch of its own, and nothing to
// render before that arrives or if it never does, since the rest of the shell already fails open
// on that same condition.
function UserIdentity() {
    const info = useSelector((state: RootState) => state.sessionInfo);
    if (!info?.username) return null;
    const identity = sessionIdentity({ mode: info.mode, username: info.username });
    return (
        <div className="app-user-identity">
            <span className="app-user-identity-name" title={identity.name}>{identity.name}</span>
            <span className="app-user-identity-kind">{identity.kind}</span>
        </div>
    );
}

function App({ pluginSidebarEntries = [] }: { pluginSidebarEntries?: PluginSidebarEntry[] }) {
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
                    <div className="app-header-right">
                        <UserIdentity />
                        <Logout />
                    </div>
                </header>
                <div className="app-body">
                    <AppErrorProvider>
                        <AccessProvider>
                            <AuthShell pluginSidebarEntries={pluginSidebarEntries} />
                        </AccessProvider>
                        <AppErrorNotification />
                    </AppErrorProvider>
                </div>
            </Provider>
        </div>
    );
}

export default App;
