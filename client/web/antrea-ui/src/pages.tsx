/**
 * Copyright 2026 Antrea Authors.
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

import { useRef, useEffect, useCallback } from 'react';
import { useSearchParams } from 'react-router';
import '@antrea/ui-components';
import { apiRefreshToken } from '@antrea/ui-components';
import { useSelector, useDispatch } from 'react-redux';
import { RootState, setToken } from './store';
import { useLogout } from './logout';

function useLitPage() {
    const token = useSelector((state: RootState) => state.token ?? '');
    const dispatch = useDispatch();
    const ref = useRef<HTMLElement>(null);
    const logout = useLogout();

    // The access token is short-lived (~10 min); the refresh token lives in a
    // 24h HTTP-only cookie. A 401 from any page just means the access token
    // expired — try a silent refresh (which relies on that cookie) before
    // giving up and sending the user back to the login screen. Only logging
    // out here would otherwise force a re-login every ~10 minutes instead of
    // the intended 24h session.
    const onSessionExpired = useCallback(async () => {
        try {
            const newToken = await apiRefreshToken();
            dispatch(setToken(newToken.accessToken));
        } catch {
            logout('Your session has expired. Please log in again.');
        }
    }, [dispatch, logout]);

    useEffect(() => {
        const el = ref.current;
        if (!el) return;
        el.addEventListener('antrea-session-expired', onSessionExpired);
        return () => el.removeEventListener('antrea-session-expired', onSessionExpired);
    }, [onSessionExpired]);

    return { ref, token };
}

export function SummaryPage() {
    const { ref, token } = useLitPage();
    return <antrea-summary-page ref={ref} token={token} />;
}

export function TraceflowPage() {
    const { ref, token } = useLitPage();
    return <antrea-traceflow-page ref={ref} token={token} />;
}

export function FlowVisibilityPage() {
    const { ref, token } = useLitPage();
    // Flow List / Service Map is switched from the left nav (see nav.tsx) instead of the
    // component's own built-in tab strip (hidden via hideViewToggle below), via a ?view=
    // query param rather than a separate route — that keeps this single page instance
    // mounted across the switch, preserving the live SSE connection and applied filters.
    const [searchParams] = useSearchParams();
    const viewMode = searchParams.get('view') === 'map' ? 'map' : 'list';
    return <antrea-flow-visibility-page ref={ref} token={token} viewMode={viewMode} hideViewToggle />;
}

export function SettingsPage() {
    const { ref, token } = useLitPage();
    return <antrea-settings-page ref={ref} token={token} />;
}
