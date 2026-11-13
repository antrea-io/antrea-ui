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
import { publishPluginAccess } from './plugins';

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
            .then((s) => {
                if (cancelled) return;
                // Published here as well as by the effect below: that one runs after the commit
                // that mounts a gated plugin page, so an element reading access() once in its
                // connectedCallback would otherwise see an unloaded, null summary.
                publishPluginAccess(true, s);
                setSummary(s);
                setLoaded(true);
            })
            // Fetch failure fails open: summary stays null, and callers treat a null summary as
            // "allow everything" — exactly today's pre-access-summary behaviour.
            .catch(() => {
                if (cancelled) return;
                publishPluginAccess(true, null);
                setSummary(null);
                setLoaded(true);
            });
        return () => { cancelled = true; };
    }, [session]);

    // Plugins read the same state through window.__antreaPluginHost.access. The fetch above
    // publishes a result as it arrives, so this effect is what publishes the reset when the
    // session changes (and repeats the result, which publishing ignores).
    useEffect(() => { publishPluginAccess(loaded, summary); }, [loaded, summary]);

    return (
        <AccessContext.Provider value={{ summary, loaded }}>
            {props.children}
        </AccessContext.Provider>
    );
}

/**
 * The name the last scope chosen is kept under: the session's username, else the one the access
 * summary resolved, else '' when neither is known. Every reader and writer of that scope must use
 * this, so they agree on the key; in admin mode the two names differ (`admin`, and the
 * impersonated ServiceAccount), so the order matters.
 */
// eslint-disable-next-line react-refresh/only-export-components
export function useScopeUser(): string {
    const sessionUser = useSelector((state: RootState) => state.sessionInfo?.username);
    const { summary } = useAccess();
    return sessionUser || summary?.username || '';
}

// eslint-disable-next-line react-refresh/only-export-components
export function useAccess(): AccessContextType {
    return useContext(AccessContext);
}
