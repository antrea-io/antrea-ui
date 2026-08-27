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

import React, { useRef, useCallback, useMemo } from 'react';
import '@antrea/ui-components';
import { can, canViewSummary, canViewOverview, GATE_TRACEFLOW_CREATE } from '@antrea/ui-components';
import type { AccessSummary, FlowVisibilityInitialFilter } from '@antrea/ui-components';
import { Navigate, useNavigate, useSearchParams } from 'react-router';
import { useLogout } from './logout';
import { getEdgeExtraRenderers, getFlowTableColumnsProcessors, getLandingPageTabs } from './plugins';
import { useAccess } from './access';

// Picks the first route the user is actually permitted to see, so a partially-authorized user
// doesn't land on a page that's just going to show the permission panel. While the access
// summary hasn't loaded yet, renders nothing.
export function HomeRedirect() {
    const { summary, loaded } = useAccess();
    if (!loaded) return null;
    if (canViewOverview(summary)) return <Navigate to="/overview" replace />;
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

// extraEvent/extraHandler let a page wire up one more DOM CustomEvent alongside
// antrea-session-expired (e.g. Overview's antrea-navigate) through the same callback ref,
// instead of every such page hand-rolling its own attach/detach bookkeeping. extraHandler must
// be stable (wrap it in useCallback) — its identity is a ref-callback dependency, so a new
// function every render would detach and reattach on every render.
function useLitPage(extraEvent?: string, extraHandler?: EventListener) {
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
        if (attachedTo.current) {
            attachedTo.current.removeEventListener('antrea-session-expired', onSessionExpired);
            if (extraEvent && extraHandler) attachedTo.current.removeEventListener(extraEvent, extraHandler);
        }
        attachedTo.current = el;
        if (el) {
            el.addEventListener('antrea-session-expired', onSessionExpired);
            if (extraEvent && extraHandler) el.addEventListener(extraEvent, extraHandler);
        }
    }, [onSessionExpired, extraEvent, extraHandler]);

    return { ref };
}

interface OverviewTab { id: string; label: string; tag?: string; }

export function OverviewPage() {
    const navigate = useNavigate();
    const [searchParams, setSearchParams] = useSearchParams();

    // The Overview Lit component owns no routing of its own (see antrea-overview-page.ts's
    // _navigateToServiceMap) — it hands off via this event the same way every page hands off a
    // dead session, and this is where that hand-off actually calls into React Router.
    const onNavigate = useCallback((e: Event) => {
        const detail = (e as CustomEvent<{ path: string; search?: Record<string, string> }>).detail;
        if (!detail) return;
        const query = detail.search ? `?${new URLSearchParams(detail.search).toString()}` : '';
        navigate(`${detail.path}${query}`);
    }, [navigate]);
    const { ref } = useLitPage('antrea-navigate', onNavigate);

    // Plugin tabs (e.g. ANS's "Security") are registered once at startup (see plugins.ts) and
    // never change afterward, so this only needs to run once per mount.
    const tabs: OverviewTab[] = useMemo(() => [{ id: 'overview', label: 'Overview' }, ...getLandingPageTabs()], []);
    const activeTab = tabs.find(t => t.id === searchParams.get('tab')) ?? tabs[0];

    return (
        <RequirePermission predicate={canViewOverview}>
            <div className="page-layout">
                {tabs.length > 1 && (
                    <div className="btn-group">
                        {tabs.map(tab => (
                            <React.Fragment key={tab.id}>
                                <antrea-button
                                    type="button"
                                    action={tab.id === activeTab.id ? 'solid' : 'outline'}
                                    onClick={() => setSearchParams(tab.id === 'overview' ? {} : { tab: tab.id })}
                                >
                                    {tab.label}
                                </antrea-button>
                            </React.Fragment>
                        ))}
                    </div>
                )}
                {activeTab.id === 'overview'
                    ? <antrea-overview-page ref={ref} />
                    : React.createElement(activeTab.tag as string, { ref })}
            </div>
        </RequirePermission>
    );
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
    const [searchParams] = useSearchParams();

    // Lets the Overview landing page deep-link here with a filter preset (see
    // antrea-overview-page.ts's _navigateToServiceMap) — undefined when none of these params are
    // present, so a plain /flows visit is unaffected. Comma-joined, matching the convention the
    // flow stream's own namespaces/pods/services query params already use (see
    // pkg/handlers/flowstream/handler.go's splitTrimmed).
    const initialFilter = useMemo<FlowVisibilityInitialFilter | undefined>(() => {
        const namespaces = searchParams.get('namespaces');
        const pods = searchParams.get('pods');
        const services = searchParams.get('services');
        const view = searchParams.get('view');
        if (!namespaces && !pods && !services && !view) return undefined;
        return {
            namespaces: namespaces ? namespaces.split(',').filter(Boolean) : undefined,
            podNames: pods ? pods.split(',').filter(Boolean) : undefined,
            serviceNames: services ? services.split(',').filter(Boolean) : undefined,
            viewMode: view === 'map' ? 'map' : view === 'list' ? 'list' : undefined,
        };
        // The Lit component only ever applies the first non-undefined value it sees (see
        // antrea-flow-visibility-page.ts's initialFilter handling), so recomputing this on every
        // searchParams change is harmless — it's just not read again after that first apply.
    }, [searchParams]);

    return (
        <antrea-flow-visibility-page
            ref={ref}
            edgeExtraRenderers={getEdgeExtraRenderers()}
            flowTableColumnsProcessors={getFlowTableColumnsProcessors()}
            initialFilter={initialFilter}
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
