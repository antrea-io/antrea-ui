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
import { useSelector } from 'react-redux';
import '@antrea/ui-components';
import { can, canViewSummary, canViewOverview, canViewFlows, sessionIdentity, GATE_TRACEFLOW_CREATE } from '@antrea/ui-components';
import type { FlowVisibilityInitialFilter } from '@antrea/ui-components';
import { Navigate, useNavigate, useSearchParams } from 'react-router';
import { useLogout } from './logout';
import { BUILTIN_LANDING_PAGE_TAB_ID, getEdgeExtraRenderers, getFlowTableColumnsProcessors, getLandingPageTabs } from './plugins';
import { useAccess } from './access';
import type { RootState } from './store';

// Picks the first route the user is actually permitted to see, so a partially-authorized user
// doesn't land on a page that's just going to show the permission panel. While the access
// summary hasn't loaded yet, renders nothing.
export function HomeRedirect() {
    const { summary, loaded } = useAccess();
    if (!loaded) return null;
    if (canViewOverview(summary)) return <Navigate to="/overview" replace />;
    if (canViewSummary(summary)) return <Navigate to="/summary" replace />;
    if (can(summary, GATE_TRACEFLOW_CREATE)) return <Navigate to="/traceflow" replace />;
    // canViewFlows is a rendering hint fed by the same RBAC the Flow Aggregator itself checks
    // (see access-api.ts), not a stand-in for its authorization decision - it can only ever
    // agree with FA's own answer or be more conservative, never grant a stream FA would refuse.
    if (canViewFlows(summary)) return <Navigate to="/flows/list" replace />;
    // A user permitted none of Summary, Traceflow or Flows lands on Settings, which needs no
    // permission at all - the floor everyone can reach.
    return <Navigate to="/settings" replace />;
}

// Wraps a page element so it only renders when `allowed` is true, matching the rule the nav uses
// to decide whether to show the tab in the first place — one rule per page, so the nav entry and
// the route guard cannot drift apart. The rule is evaluated by the caller rather than passed in as
// a predicate over the access summary, so that a gate needing more than the summary to answer can
// reuse the same wrapper. While the answer hasn't loaded yet, renders nothing rather than either
// the page or the permission panel.
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
    const { summary, loaded } = useAccess();
    // The same identity the app header's UserIdentity derives its display from (App.tsx) — a
    // friendly name, not the raw Kubernetes username (e.g. a ServiceAccount's is just the
    // "<namespace>:<name>" part, and the static admin login's is its local login name).
    const sessionInfo = useSelector((state: RootState) => state.sessionInfo);
    const welcomeName = sessionInfo?.username
        ? sessionIdentity({ mode: sessionInfo.mode, username: sessionInfo.username }).name
        : undefined;

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
    const tabs: OverviewTab[] = useMemo(() => [{ id: BUILTIN_LANDING_PAGE_TAB_ID, label: 'Network Traffic & Inventory' }, ...getLandingPageTabs()], []);
    const activeTab = tabs.find(t => t.id === searchParams.get('tab')) ?? tabs[0];

    return (
        <RequirePermission allowed={canViewOverview(summary)} loaded={loaded}>
            <div className="page-layout">
                <p className="page-title">{welcomeName ? `Welcome, ${welcomeName}` : 'Welcome'}</p>
                {/* Shown even with only the built-in tab: it names what's below, and keeps the
                    layout stable whether or not a plugin has registered tabs. */}
                <div className="tab-bar">
                    {tabs.map(tab => (
                        <button
                            key={tab.id}
                            type="button"
                            className={`tab-bar-item${tab.id === activeTab.id ? ' active' : ''}`}
                            onClick={() => setSearchParams(tab.id === BUILTIN_LANDING_PAGE_TAB_ID ? {} : { tab: tab.id })}
                        >
                            {tab.label}
                        </button>
                    ))}
                </div>
                {activeTab.id === BUILTIN_LANDING_PAGE_TAB_ID
                    ? <antrea-overview-page ref={ref} />
                    : React.createElement(activeTab.tag as string, { ref })}
            </div>
        </RequirePermission>
    );
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
    const { summary, loaded } = useAccess();
    const [searchParams] = useSearchParams();

    // Lets the Overview landing page deep-link here with a filter preset (see
    // antrea-overview-page.ts's _navigateToServiceMap) — undefined when none of these params are
    // present, so a plain /flows/list or /flows/map visit is unaffected. Comma-joined, matching
    // the convention the flow stream's own namespaces/pods/services query params already use
    // (see pkg/handlers/flowstream/handler.go's splitTrimmed). Which view renders is the route
    // itself (the `view` prop above, from index.tsx's flows/list and flows/map routes), not a
    // query param here.
    const initialFilter = useMemo<FlowVisibilityInitialFilter | undefined>(() => {
        const namespaces = searchParams.get('namespaces');
        const pods = searchParams.get('pods');
        const services = searchParams.get('services');
        if (!namespaces && !pods && !services) return undefined;
        return {
            namespaces: namespaces ? namespaces.split(',').filter(Boolean) : undefined,
            podNames: pods ? pods.split(',').filter(Boolean) : undefined,
            serviceNames: services ? services.split(',').filter(Boolean) : undefined,
        };
        // The Lit component only ever applies the first non-undefined value it sees (see
        // antrea-flow-visibility-page.ts's initialFilter handling), so recomputing this on every
        // searchParams change is harmless — it's just not read again after that first apply.
    }, [searchParams]);

    return (
        <RequirePermission allowed={canViewFlows(summary)} loaded={loaded}>
            <antrea-flow-visibility-page
                ref={ref}
                viewMode={view}
                edgeExtraRenderers={getEdgeExtraRenderers()}
                flowTableColumnsProcessors={getFlowTableColumnsProcessors()}
                initialFilter={initialFilter}
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
