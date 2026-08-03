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

import React, { useRef, useEffect, useCallback } from 'react';
import '@antrea/ui-components';
import { useLogout } from './logout';
import { getEdgeExtraRenderers, getFlowTableColumnsProcessors } from './plugins';

function useLitPage() {
    const ref = useRef<HTMLElement>(null);
    const logout = useLogout();

    // A 401 from a page now means the session is genuinely gone: idle-expired, past its 12h
    // lifetime cap, logged out in another tab, the backend restarted, or the identity provider
    // revoked the refresh token. There is no short-lived access token to renew any more —
    // credential refresh happens server-side, and the backend has already attempted the only
    // refresh that exists — so a single 401 is authoritative and there is nothing to retry.
    //
    // A 403 is a different thing entirely (the user is logged in but lacks the Kubernetes RBAC
    // for that call) and never reaches here: pages only dispatch this event for a 401.
    const onSessionExpired = useCallback(() => {
        logout('Your session has expired. Please log in again.');
    }, [logout]);

    useEffect(() => {
        const el = ref.current;
        if (!el) return;
        el.addEventListener('antrea-session-expired', onSessionExpired);
        return () => el.removeEventListener('antrea-session-expired', onSessionExpired);
    }, [onSessionExpired]);

    return { ref };
}

export function SummaryPage() {
    const { ref } = useLitPage();
    return <antrea-summary-page ref={ref} />;
}

export function TraceflowPage() {
    const { ref } = useLitPage();
    return <antrea-traceflow-page ref={ref} />;
}

export function FlowVisibilityPage() {
    const { ref } = useLitPage();
    return (
        <antrea-flow-visibility-page
            ref={ref}
            edgeExtraRenderers={getEdgeExtraRenderers()}
            flowTableColumnsProcessors={getFlowTableColumnsProcessors()}
        />
    );
}

export function SettingsPage() {
    const { ref } = useLitPage();
    return <antrea-settings-page ref={ref} />;
}

// Generic route element for plugin pages: any plugin calling registerRoute() (see plugins.ts)
// gets its custom element mounted here, with the same ref/session-expiry wiring as built-in
// pages, keyed off the tag name discovered at runtime instead of a compile-time import.
export function PluginPage({ tag }: { tag: string }) {
    const { ref } = useLitPage();
    return React.createElement(tag, { ref });
}
