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
import { accessSummary, hasFlowPermissions } from '@antrea/ui-components';
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

// Whether the caller is the built-in admin, a Kubernetes cluster admin, or otherwise already
// holds flows cluster-wide on their own. TEMPORARY: mirrors the admin-only condition
// requireFlowVisibility used to enforce on the backend, reinstated here as a frontend-only
// rendering hint while the observed-Namespace selector doesn't exist yet - without it,
// clusterWide=true is the only scope Flow Visibility can ever request, so anyone else's stream is
// either fully disclosed (if they hold flows cluster-wide, the third check below) or a 403 (if
// they don't) - see StreamAuthorization.clusterWide upstream. Remove once the selector lands and
// a non-admin can request their own Namespace's scope instead.
//
// clusterAdmin alone is not enough: the built-in admin-password session impersonates the
// antrea-ui-admin ServiceAccount, which holds no */*/* rule, so it reports clusterAdmin: false.
//
// useAccess()'s summary is always evaluated at cluster scope (accessSummary() passes no
// namespace), which is what makes hasFlowPermissions meaningful here - see its own doc comment.
// eslint-disable-next-line react-refresh/only-export-components
export function useIsAdmin(): boolean {
    const mode = useSelector((state: RootState) => state.sessionInfo?.mode);
    const { summary } = useAccess();
    return mode === 'admin' || summary?.clusterAdmin === true || hasFlowPermissions(summary);
}
