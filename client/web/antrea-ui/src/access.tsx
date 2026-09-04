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
// TEMPORARY, mirrors requireFlowVisibility() in pkg/server/api/flowstream.go — the backend is
// authoritative and 403s regardless. Fails *closed*, unlike the can() gates in access-api.ts:
// those mirror RBAC the API server enforces anyway, so allowing on a missing answer just lets the
// API server say no; here allowing would render a page guaranteed to error.
//
// sessionInfo is null when the login page's own GET /auth/session failed, which also fails closed;
// a reload recovers it.
//
// eslint-disable-next-line react-refresh/only-export-components
export function useCanViewFlows(): { allowed: boolean, loaded: boolean } {
    const { summary, loaded } = useAccess();
    const info = useSelector((state: RootState) => state.sessionInfo);
    return { allowed: info?.mode === 'admin' || summary?.clusterAdmin === true, loaded };
}
