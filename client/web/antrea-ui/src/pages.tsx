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
import {
    can, canViewSummary, canViewOverview, holdsOverviewResources, canViewFlows, holdsFlowsClusterWide,
    sessionIdentity, GATE_TRACEFLOW_CREATE, SCOPE_CHANGE_EVENT, SCOPE_CHOSEN_EVENT,
} from '@antrea/ui-components';
import type { ScopeChangeDetail, ScopeChosenDetail } from '@antrea/ui-components';
import { Navigate, useLocation, useSearchParams } from 'react-router';
import { getLastScope, scopeSearch, setLastScope } from './lastScope';
import { useLogout } from './logout';
import { BUILTIN_LANDING_PAGE_TAB_ID, getEdgeExtraRenderers, getFlowTableColumnsProcessors } from './plugins';
import { useAccess, useScopeUser } from './access';
import type { LandingPageTab } from './plugins';
import type { RootState } from './store';

// Picks the first route the user is actually permitted to see, so a partially-authorized user
// doesn't land on a page that's just going to show the permission panel. While the access
// summary hasn't loaded yet, renders nothing.
//
// The Overview comes first only for a user whose cluster-scoped summary grants something it shows.
// A user who merely appears in some RoleBinding (canViewOverview's wider test, from the
// summary.namespaces heuristic) may have a working Summary, Traceflow or Flows grant and no
// access to anything the Overview lists, so that case ranks after those: still before Settings,
// since the page may have something for them, which they can find by picking a namespace.
export function HomeRedirect() {
    const { summary, loaded } = useAccess();
    const user = useScopeUser();
    if (!loaded) return null;
    // The last scope chosen goes with the redirect to a Namespace-aware page, like a navigation
    // entry's link.
    const lastScope = scopeSearch(getLastScope(user));
    if (holdsOverviewResources(summary)) return <Navigate to={`/overview${lastScope}`} replace />;
    if (canViewSummary(summary)) return <Navigate to="/summary" replace />;
    if (can(summary, GATE_TRACEFLOW_CREATE)) return <Navigate to="/traceflow" replace />;
    // holdsFlowsClusterWide is a rendering hint fed by the same RBAC the Flow Aggregator itself
    // checks (see access-api.ts), not a stand-in for its authorization decision - it can only ever
    // agree with FA's own answer or be more conservative, never grant a stream FA would refuse.
    // Only the cluster-wide holder lands here: canViewFlows is wider, and a user who merely has a
    // RoleBinding may be refused by FA, so that case falls through to canViewOverview.
    if (holdsFlowsClusterWide(summary)) {
        return <Navigate to={`/flows/list${lastScope}`} replace />;
    }
    if (canViewOverview(summary)) return <Navigate to={`/overview${lastScope}`} replace />;
    // A user permitted none of these lands on Settings, which needs no permission at all - the
    // floor everyone can reach.
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

interface OverviewTab { id: string; label: string; tag?: string; }
// A stable default, so that omitting the prop does not give useMemo a new dependency every render.
const NO_PLUGIN_TABS: LandingPageTab[] = [];

// The query string of a tab: its own `tab`, and the Namespace scope the page has, which belongs to
// the page and not to a tab, so switching between tabs keeps it.
function tabSearch(current: URLSearchParams, tabId: string): URLSearchParams {
    const next = new URLSearchParams();
    for (const scope of current.getAll('namespace')) next.append('namespace', scope);
    if (tabId !== BUILTIN_LANDING_PAGE_TAB_ID) next.set('tab', tabId);
    return next;
}

// `pluginTabs` are the plugin-registered landing page tabs. They are registered once at startup
// (see plugins.ts), so main() resolves them once, with the routes and sidebar entries, and passes
// them down instead of this page resolving them on every mount.
export function OverviewPage({ pluginTabs = NO_PLUGIN_TABS }: { pluginTabs?: LandingPageTab[] }) {
    const [searchParams, setSearchParams] = useSearchParams();
    const { summary, loaded } = useAccess();
    // The same identity the app header's UserIdentity derives its display from (App.tsx) — a
    // friendly name, not the raw Kubernetes username (e.g. a ServiceAccount's is just the
    // "<namespace>:<name>" part, and the static admin login's is its local login name).
    const sessionInfo = useSelector((state: RootState) => state.sessionInfo);
    const welcomeName = sessionInfo?.username
        ? sessionIdentity({ mode: sessionInfo.mode, username: sessionInfo.username }).name
        : undefined;

    const { ref, search } = useNamespaceScope();

    const tabs: OverviewTab[] = useMemo(() => [{ id: BUILTIN_LANDING_PAGE_TAB_ID, label: 'Inventory & Events' }, ...pluginTabs], [pluginTabs]);
    const activeTab = tabs.find(t => t.id === searchParams.get('tab')) ?? tabs[0];

    return (
        <RequirePermission allowed={canViewOverview(summary)} loaded={loaded}>
            <div className="page-layout">
                {/* page-header, not bare: the tiles below sit in the Lit page's <main>, which
                    pads its content, and every other page renders its title there too. */}
                <div className="page-header">
                    <p className="page-title">{welcomeName ? `Welcome, ${welcomeName}` : 'Welcome'}</p>
                    {/* Shown even with only the built-in tab: it names what's below, and keeps the
                        layout stable whether or not a plugin has registered tabs. */}
                    <div className="tab-bar">
                        {tabs.map(tab => (
                            <button
                                key={tab.id}
                                type="button"
                                className={`tab-bar-item${tab.id === activeTab.id ? ' active' : ''}`}
                                onClick={() => setSearchParams(tabSearch(searchParams, tab.id))}
                            >
                                {tab.label}
                            </button>
                        ))}
                    </div>
                </div>
                {activeTab.id === BUILTIN_LANDING_PAGE_TAB_ID
                    ? <antrea-overview-page ref={ref} locationSearch={search} />
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

// What a Namespace-aware page needs from its host. The page keeps its scope in the query string
// (`?namespace=`), and rewrites it with history.replaceState, which the router is not told about.
// It says so with SCOPE_CHANGE_EVENT instead, and the router adopts the location here, so the
// sidebar's links never go stale. The location goes the other way too: the page is handed the
// query string (`search`), and follows it when it changes. A scope the page takes from its picker
// or its URL is remembered as the last scope chosen (see lastScope.ts), which the navigation
// entries carry.
function useNamespaceScope() {
    const { ref: litRef } = useLitPage();
    const { search } = useLocation();
    const [, setSearchParams] = useSearchParams();
    const user = useScopeUser();
    const attachedTo = useRef<HTMLElement | null>(null);
    const onScopeChange = useCallback((e: Event) => {
        const { search } = (e as CustomEvent<ScopeChangeDetail>).detail;
        setSearchParams(new URLSearchParams(search), { replace: true });
    }, [setSearchParams]);
    const onScopeChosen = useCallback((e: Event) => {
        setLastScope(user, (e as CustomEvent<ScopeChosenDetail>).detail.scope);
    }, [user]);
    // A callback ref for the same reason as useLitPage's: the element mounts once the permission
    // check resolves, which does not re-render a hook declared here to notice it.
    const ref = useCallback((el: HTMLElement | null) => {
        litRef(el);
        attachedTo.current?.removeEventListener(SCOPE_CHANGE_EVENT, onScopeChange);
        attachedTo.current?.removeEventListener(SCOPE_CHOSEN_EVENT, onScopeChosen);
        attachedTo.current = el;
        el?.addEventListener(SCOPE_CHANGE_EVENT, onScopeChange);
        el?.addEventListener(SCOPE_CHOSEN_EVENT, onScopeChosen);
    }, [litRef, onScopeChange, onScopeChosen]);
    return { ref, search };
}

// view is the route's own sub-page (see index.tsx's "flows/list" / "flows/map" routes) — the sole
// source of truth for which one is showing, flowing one-way into the Lit element's viewMode
// property. There is no in-page control that could disagree with it: switching is entirely a
// sidebar (nav.tsx) concern. Switching between the views does not remount the page, which follows
// its query string (see useNamespaceScope).
export function FlowVisibilityPage({ view }: { view: 'list' | 'map' }) {
    const { ref, search } = useNamespaceScope();
    const { summary, loaded } = useAccess();
    return (
        <RequirePermission allowed={canViewFlows(summary)} loaded={loaded}>
            <antrea-flow-visibility-page
                ref={ref}
                viewMode={view}
                locationSearch={search}
                edgeExtraRenderers={getEdgeExtraRenderers()}
                flowTableColumnsProcessors={getFlowTableColumnsProcessors()}
            />
        </RequirePermission>
    );
}

// "/flows" itself just lands on the default sub-page, and keeps the query string, which is where
// the page's scope lives.
export function FlowsRedirect() {
    const { search } = useLocation();
    return <Navigate to={{ pathname: '/flows/list', search }} replace />;
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
