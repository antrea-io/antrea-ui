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

import React, { useRef, useCallback } from 'react';
import '@antrea/ui-components';
import { can, canViewSummary, GATE_TRACEFLOW_CREATE } from '@antrea/ui-components';
import type { AccessSummary } from '@antrea/ui-components';
import { Navigate } from 'react-router';
import { useLogout } from './logout';
import { getEdgeExtraRenderers, getFlowTableColumnsProcessors } from './plugins';
import { useAccess } from './access';

// Picks the first route the user is actually permitted to see, so a partially-authorized user
// doesn't land on a Summary page that's just going to show the permission panel. While the
// access summary hasn't loaded yet, renders nothing.
export function HomeRedirect() {
    const { summary, loaded } = useAccess();
    if (!loaded) return null;
    if (canViewSummary(summary)) return <Navigate to="/summary" replace />;
    if (can(summary, GATE_TRACEFLOW_CREATE)) return <Navigate to="/traceflow" replace />;
    // Flow Visibility has no per-user RBAC today (see docs/authentication.md), so it is always
    // eligible and is the practical floor here.
    return <Navigate to="/flows" replace />;
}

// Wraps a page element so it only renders when predicate(summary) is true, matching the
// predicate the nav uses to decide whether to show the tab in the first place — one predicate per
// page, so the nav entry and the route guard cannot drift apart. While the access summary hasn't
// loaded yet, renders nothing rather than either the page or the permission panel.
function RequirePermission({ predicate, children }: { predicate: (s: AccessSummary | null) => boolean, children: React.ReactNode }) {
    const { summary, loaded } = useAccess();
    if (!loaded) return null;
    if (!predicate(summary)) {
        return (
            <antrea-alert status="warning">
                You do not have permission to view this page.
            </antrea-alert>
        );
    }
    return <>{children}</>;
}

function useLitPage() {
    const logout = useLogout();
    const attachedTo = useRef<HTMLElement | null>(null);

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

    // A callback ref, not useRef + useEffect: the page element mounts once a permission check
    // elsewhere (RequirePermission) resolves and stops returning null, which re-renders that
    // *descendant*, not this component — a useEffect declared here would never re-run to notice
    // ref.current going from null to the element. A callback ref fires exactly when React
    // attaches or detaches the DOM node, regardless of which component's render caused it.
    const ref = useCallback((el: HTMLElement | null) => {
        if (attachedTo.current) attachedTo.current.removeEventListener('antrea-session-expired', onSessionExpired);
        attachedTo.current = el;
        if (el) el.addEventListener('antrea-session-expired', onSessionExpired);
    }, [onSessionExpired]);

    return { ref };
}

export function SummaryPage() {
    const { ref } = useLitPage();
    return (
        <RequirePermission predicate={canViewSummary}>
            <antrea-summary-page ref={ref} />
        </RequirePermission>
    );
}

export function TraceflowPage() {
    const { ref } = useLitPage();
    return (
        <RequirePermission predicate={(s) => can(s, GATE_TRACEFLOW_CREATE)}>
            <antrea-traceflow-page ref={ref} />
        </RequirePermission>
    );
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
