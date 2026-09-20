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
import { accessSummary, flowNamespaces } from '@antrea/ui-components';
import type { AccessSummary, FlowNamespacesResponse } from '@antrea/ui-components';
import { useSelector } from 'react-redux';
import type { RootState } from './store';

interface AccessContextType {
    summary: AccessSummary | null
    /** Which namespaces this caller may observe flows in, or null while unknown. Separate from
     * the access summary because Kubernetes offers no reverse lookup from a subject to the
     * namespaces it may access: the backend has to enumerate candidates and review each one, so
     * it cannot come from the summary's rules. Null means "not known yet or the fetch failed",
     * which canViewFlows treats as allow. */
    flowNs: FlowNamespacesResponse | null
    loaded: boolean
}

const AccessContext = React.createContext<AccessContextType>({ summary: null, flowNs: null, loaded: false });

export function AccessProvider(props: React.PropsWithChildren) {
    const session = useSelector((state: RootState) => state.session);
    const [summary, setSummary] = useState<AccessSummary | null>(null);
    const [flowNs, setFlowNs] = useState<FlowNamespacesResponse | null>(null);
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
        setFlowNs(null);
        setLoaded(false);
    }

    useEffect(() => {
        if (session !== 'authenticated') return;
        let cancelled = false;
        // Both fail open, and `loaded` waits for both: an entry appearing once the answers are
        // in reads better than one vanishing when a restriction turns out to apply. The flow
        // namespaces are fetched here rather than left to the page so the nav can gate on the
        // same answer the page will act on; the backend caches and single-flights it per
        // session, so the page fetching it again costs nothing.
        Promise.allSettled([accessSummary(), flowNamespaces()])
            .then(([s, f]) => {
                if (cancelled) return;
                setSummary(s.status === 'fulfilled' ? s.value : null);
                setFlowNs(f.status === 'fulfilled' ? f.value : null);
                setLoaded(true);
            });
        return () => { cancelled = true; };
    }, [session]);

    return (
        <AccessContext.Provider value={{ summary, flowNs, loaded }}>
            {props.children}
        </AccessContext.Provider>
    );
}

// eslint-disable-next-line react-refresh/only-export-components
export function useAccess(): AccessContextType {
    return useContext(AccessContext);
}
