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
import { Navigate } from 'react-router';
import { useLogout } from './logout';
import { getEdgeExtraRenderers, getFlowTableColumnsProcessors } from './plugins';
import { useAccess, useCanViewFlows } from './access';

// Picks the first route the user is actually permitted to see, so a partially-authorized user
// doesn't land on a Summary page that's just going to show the permission panel. While the
// access summary hasn't loaded yet, renders nothing.
export function HomeRedirect() {
    const { summary, loaded } = useAccess();
    const { allowed: canViewFlows } = useCanViewFlows();
    if (!loaded) return null;
    if (canViewSummary(summary)) return <Navigate to="/summary" replace />;
    if (can(summary, GATE_TRACEFLOW_CREATE)) return <Navigate to="/traceflow" replace />;
    // Flow Visibility goes straight to its default sub-page (Flow List) rather than "/flows",
    // which only exists to redirect there itself. It is no longer the floor: it is restricted to
    // admins for now (see useCanViewFlows), so a user permitted none of the three lands on
    // Settings, which the nav always shows and which needs no permission.
    if (canViewFlows) return <Navigate to="/flows/list" replace />;
    return <Navigate to="/settings" replace />;
}

// Wraps a page element so it only renders when `allowed` is true, matching the rule the nav uses
// to decide whether to show the tab in the first place — one rule per page, so the nav entry and
// the route guard cannot drift apart. The rule is evaluated by the caller rather than passed in as
// a predicate over the access summary, so that a gate needing more than the summary (Flow
// Visibility also needs the session mode) can reuse the same wrapper. While the answer hasn't
// loaded yet, renders nothing rather than either the page or the permission panel.
function RequirePermission({ allowed, loaded, children }: { allowed: boolean, loaded: boolean, children: React.ReactNode }) {
    if (!loaded) return null;
    if (!allowed) {
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
    const { summary, loaded } = useAccess();
    return (
        <RequirePermission allowed={canViewSummary(summary)} loaded={loaded}>
            <antrea-summary-page ref={ref} />
        </RequirePermission>
    );
}

export function TraceflowPage() {
    const { ref } = useLitPage();
    const { summary, loaded } = useAccess();
    return (
        <RequirePermission allowed={can(summary, GATE_TRACEFLOW_CREATE)} loaded={loaded}>
            <antrea-traceflow-page ref={ref} />
        </RequirePermission>
    );
}

// view is the route's own sub-page (see index.tsx's "flows/list" / "flows/map" routes) — the sole
// source of truth for which one is showing, flowing one-way into the Lit element's viewMode
// property. There is no in-page control that could disagree with it: switching is entirely a
// sidebar (nav.tsx) concern.
export function FlowVisibilityPage({ view }: { view: 'list' | 'map' }) {
    const { ref } = useLitPage();
    const { allowed, loaded } = useCanViewFlows();
    return (
        <RequirePermission allowed={allowed} loaded={loaded}>
            <antrea-flow-visibility-page
                ref={ref}
                viewMode={view}
                edgeExtraRenderers={getEdgeExtraRenderers()}
                flowTableColumnsProcessors={getFlowTableColumnsProcessors()}
            />
        </RequirePermission>
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
