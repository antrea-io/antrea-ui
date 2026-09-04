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

import React, { useState, useContext, useEffect } from 'react';
import { accessSummary } from '@antrea/ui-components';
import type { AccessSummary } from '@antrea/ui-components';
import { useSelector } from 'react-redux';
import type { RootState } from './store';

interface AccessContextType {
    summary: AccessSummary | null
    loaded: boolean
}

const AccessContext = React.createContext<AccessContextType>({ summary: null, loaded: false });

export function AccessProvider(props: React.PropsWithChildren) {
    const session = useSelector((state: RootState) => state.session);
    const [summary, setSummary] = useState<AccessSummary | null>(null);
    const [loaded, setLoaded] = useState(false);

    // Drop what we hold whenever the session changes, rather than leaving it in place until the
    // effect below resolves: an anonymous -> authenticated transition within one page lifetime
    // would otherwise render the previous user's permissions to the new one. Today's logout
    // navigates the whole page away, so that transition is unreachable, but the safety should
    // not depend on that. Adjusted during render (the React-documented pattern for derived
    // state) because doing it in the effect would render one frame with the stale values.
    const [sessionForSummary, setSessionForSummary] = useState(session);
    if (sessionForSummary !== session) {
        setSessionForSummary(session);
        setSummary(null);
        setLoaded(false);
    }

    useEffect(() => {
        if (session !== 'authenticated') return;
        let cancelled = false;
        accessSummary()
            .then((s) => { if (!cancelled) { setSummary(s); setLoaded(true); } })
            // Fetch failure fails open: summary stays null, and callers treat a null summary as
            // "allow everything" — exactly today's pre-access-summary behaviour.
            .catch(() => { if (!cancelled) { setSummary(null); setLoaded(true); } });
        return () => { cancelled = true; };
    }, [session]);

    return (
        <AccessContext.Provider value={{ summary, loaded }}>
            {props.children}
        </AccessContext.Provider>
    );
}

// eslint-disable-next-line react-refresh/only-export-components
export function useAccess(): AccessContextType {
    return useContext(AccessContext);
}

// Whether the caller may view flow data: the built-in admin, or a Kubernetes cluster admin.
//
// TEMPORARY, mirrors requireFlowVisibility() in pkg/server/api/flowstream.go. That middleware is
// the authorization decision and it fails closed on its own: every path that is not an allow calls
// c.Abort(), so the backend never subscribes to the Flow Aggregator for a caller it rejected. This
// hook only decides whether to render UI that would otherwise just 403, which is why it can fail
// *open* on a missing answer like the can() gates in access-api.ts do.
//
// A definite `clusterAdmin: false` hides the page. A null summary means the access-summary fetch
// failed (AccessProvider does not retry within a page lifetime), so the backend has not answered at
// all, and denying here would leave a cluster admin looking at the generic permission panel for the
// rest of the page lifetime with nothing pointing at "permissions failed to load". Allowing instead
// defers to requireFlowVisibility, whose 403 onForbidden renders as a terminal panel naming the
// actual restriction — a better answer either way.
//
// A null sessionInfo allows for the same reason, and with more force. It means the login page's
// own GET /auth/session failed, which _submit swallows (the login itself succeeded, there is just
// nothing to display for it), so a built-in admin can reach an authenticated app with mode
// unknown. Their summary then reports clusterAdmin false — correctly, the antrea-ui-admin
// ServiceAccount holds no */*/* rule — and denying on it would hide the page from precisely the
// caller requireFlowVisibility short-circuits to an allow. Unlike a null summary, this is not even
// an unknown answer on the backend's side: it is one the backend resolves in the user's favour.
// The cost is the same as above, plus the initial redirect: HomeRedirect reads this hook too, so
// such a caller lands on the flows page and meets the 403 panel there instead of Settings. Both
// only during a failure that also broke their session probe.
//
// eslint-disable-next-line react-refresh/only-export-components
export function useCanViewFlows(): { allowed: boolean, loaded: boolean } {
    const { summary, loaded } = useAccess();
    const info = useSelector((state: RootState) => state.sessionInfo);
    return { allowed: info === null || info.mode === 'admin' || summary === null || summary.clusterAdmin === true, loaded };
}
