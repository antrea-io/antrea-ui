/**
 * Copyright 2023 Antrea Authors.
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

import React from 'react';
import { useLocation } from 'react-router';
import { Link } from 'react-router';
import '@antrea/ui-components';
import { can, canViewSummary, GATE_TRACEFLOW_CREATE } from '@antrea/ui-components';
import type { PluginSidebarEntry } from './plugins';
import { useAccess, useCanViewFlows } from './access';

function DashboardIcon() {
    return (
        <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor" aria-hidden="true" style={{ flexShrink: 0 }}>
            <path d="M1 2a1 1 0 0 1 1-1h5a1 1 0 0 1 1 1v5a1 1 0 0 1-1 1H2a1 1 0 0 1-1-1V2zm8-1h5a1 1 0 0 1 1 1v5a1 1 0 0 1-1 1H9a1 1 0 0 1-1-1V2a1 1 0 0 1 1-1zM1 9a1 1 0 0 1 1-1h5a1 1 0 0 1 1 1v5a1 1 0 0 1-1 1H2a1 1 0 0 1-1-1V9zm8-1h5a1 1 0 0 1 1 1v5a1 1 0 0 1-1 1H9a1 1 0 0 1-1-1V9a1 1 0 0 1 1-1z"/>
        </svg>
    );
}

function TraceflowIcon() {
    return (
        <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor" aria-hidden="true" style={{ flexShrink: 0 }}>
            <path d="M6 1a5 5 0 1 0 3.54 8.54l3.46 3.46 1.42-1.42-3.46-3.46A5 5 0 0 0 6 1zm0 1.5a3.5 3.5 0 1 1 0 7 3.5 3.5 0 0 1 0-7z"/>
        </svg>
    );
}

function EyeIcon() {
    return (
        <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor" aria-hidden="true" style={{ flexShrink: 0 }}>
            <path d="M16 8s-3-5.5-8-5.5S0 8 0 8s3 5.5 8 5.5S16 8 16 8zM8 12a4 4 0 1 1 0-8 4 4 0 0 1 0 8zm0-2a2 2 0 1 0 0-4 2 2 0 0 0 0 4z"/>
        </svg>
    );
}

function isExternalUrl(path: string): boolean {
    return /^[a-z][a-z0-9+.-]*:\/\//i.test(path);
}

function stripLeadingSlash(path: string): string {
    return path.replace(/^\//, '');
}

// Both operands are normalized before comparing: a plugin's registered path or parentPath may or
// may not have a leading slash (dedupeByPath / plugins.ts's resolveParentPaths accept either),
// while `pathname` from useLocation() always does.
function pathStartsWith(pathname: string, path: string): boolean {
    return stripLeadingSlash(pathname).startsWith(stripLeadingSlash(path));
}
function pathEquals(pathname: string, path: string): boolean {
    return stripLeadingSlash(pathname) === stripLeadingSlash(path);
}

function GearIcon() {
    return (
        <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor" aria-hidden="true" style={{ flexShrink: 0 }}>
            <path d="M8 4.754a3.246 3.246 0 1 0 0 6.492 3.246 3.246 0 0 0 0-6.492zM5.754 8a2.246 2.246 0 1 1 4.492 0 2.246 2.246 0 0 1-4.492 0z"/>
            <path d="M9.796 1.343c-.527-1.79-3.065-1.79-3.592 0l-.094.319a.873.873 0 0 1-1.255.52l-.292-.16c-1.64-.892-3.433.902-2.54 2.541l.159.292a.873.873 0 0 1-.52 1.255l-.319.094c-1.79.527-1.79 3.065 0 3.592l.319.094a.873.873 0 0 1 .52 1.255l-.16.292c-.892 1.64.901 3.434 2.541 2.54l.292-.159a.873.873 0 0 1 1.255.52l.094.319c.527 1.79 3.065 1.79 3.592 0l.094-.319a.873.873 0 0 1 1.255-.52l.292.16c1.64.892 3.433-.902 2.54-2.541l-.159-.292a.873.873 0 0 1 .52-1.255l.319-.094c1.79-.527 1.79-3.065 0-3.592l-.319-.094a.873.873 0 0 1-.52-1.255l.16-.292c.892-1.64-.902-3.433-2.541-2.54l-.292.159a.873.873 0 0 1-1.255-.52l-.094-.319zm-2.633.283c.246-.835 1.428-.835 1.674 0l.094.319a1.873 1.873 0 0 0 2.693 1.115l.291-.16c.764-.415 1.6.42 1.184 1.185l-.159.292a1.873 1.873 0 0 0 1.116 2.692l.318.094c.835.246.835 1.428 0 1.674l-.319.094a1.873 1.873 0 0 0-1.115 2.693l.16.291c.415.764-.42 1.6-1.185 1.184l-.291-.159a1.873 1.873 0 0 0-2.693 1.116l-.094.318c-.246.835-1.428.835-1.674 0l-.094-.319a1.873 1.873 0 0 0-2.692-1.115l-.292.16c-.764.415-1.6-.42-1.184-1.185l.159-.291A1.873 1.873 0 0 0 1.945 8.93l-.319-.094c-.835-.246-.835-1.428 0-1.674l.319-.094A1.873 1.873 0 0 0 3.06 4.377l-.16-.292c-.415-.764.42-1.6 1.185-1.184l.292.159a1.873 1.873 0 0 0 2.692-1.115l.094-.319z"/>
        </svg>
    );
}

// Renders a single plugin sidebar entry as an antrea-nav-item — used both for top-level plugin
// entries and for entries nested under a group via parentPath (see plugins.ts's
// resolveParentPaths).
function renderPluginNavItem(entry: PluginSidebarEntry, pathname: string) {
    const external = isExternalUrl(entry.path);
    const label = (
        <>
            {entry.icon && (
                <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor" aria-hidden="true" style={{ flexShrink: 0 }}>
                    <path d={entry.icon} />
                </svg>
            )}
            <span className="nav-label">{entry.label}</span>
        </>
    );
    return (
        <antrea-nav-item
            key={entry.path}
            {...(!external && pathStartsWith(pathname, entry.path) ? { active: true } : {})}
        >
            {external ? (
                <a href={entry.path} target="_blank" rel="noopener noreferrer">
                    {label}
                </a>
            ) : (
                <Link to={entry.path}>{label}</Link>
            )}
        </antrea-nav-item>
    );
}

export default function NavTab({ pluginSidebarEntries }: { pluginSidebarEntries: PluginSidebarEntry[] }) {
    const { pathname } = useLocation();
    const { summary, loaded } = useAccess();

    // While the access summary hasn't loaded yet, render no core items: entries popping in once
    // loaded reads better than entries vanishing if the answer turns out to restrict something.
    const showSummary = loaded && canViewSummary(summary);
    const showTraceflow = loaded && can(summary, GATE_TRACEFLOW_CREATE);
    // Flow Visibility is gated on a rule of its own (useCanViewFlows), not on the access summary
    // alone, but it hides on the same "wait for loaded" terms as the two above.
    const { allowed: canViewFlows, loaded: flowsLoaded } = useCanViewFlows();
    const showFlows = flowsLoaded && canViewFlows;

    // Plugin entries with a parentPath (already resolved/normalized by plugins.ts's
    // resolveParentPaths — always a leading-slash-stripped path, whether that path belongs to a
    // built-in page or another plugin's own top-level entry) are nested, not rendered inline.
    const childrenByParent = new Map<string, PluginSidebarEntry[]>();
    for (const entry of pluginSidebarEntries) {
        if (!entry.parentPath) continue;
        const siblings = childrenByParent.get(entry.parentPath) ?? [];
        siblings.push(entry);
        childrenByParent.set(entry.parentPath, siblings);
    }
    const topLevelPluginEntries = pluginSidebarEntries.filter((entry) => !entry.parentPath);

    // Wraps `item` (a single antrea-nav-item, for `path`'s own page) in an antrea-nav-group
    // covering `builtinChildren` (Flow Visibility's own Flow List / Service Map, keyed and active-
    // checked the same way as a plugin's, so both sources share one group) plus any plugin
    // entries registered under `path`, so both built-in pages and plugin top-level entries get
    // the same nested-nav treatment for free. Returns `item` unchanged when nothing nests under it.
    //
    // `show` is false when `path`'s own page isn't rendered at all — gated off by RBAC (Summary,
    // Traceflow) or by the interim admin-only rule (Flow Visibility), or the access summary hasn't
    // loaded yet. plugins.ts's resolveParentPaths accepts a gated built-in page as a valid parent
    // unconditionally (it has no way to know it's gated for a given user), so a nested *plugin*
    // child would otherwise have no render site.
    //
    // Those two `!show` causes are deliberately not treated alike (closing over `loaded` directly,
    // rather than taking it as a parameter — every gated caller's own `show` is conjoined with this
    // same `loaded`, Flow Visibility's by way of useCanViewFlows re-exporting it unchanged, so if
    // that hook ever gates its `loaded` on more, this closure has to take it as a parameter):
    // "the parent is definitely gated off for this user"
    // (`loaded`) promotes its plugin children to top level, so they don't silently disappear,
    // while "we don't know yet" (`!loaded`) hides them too, consistent
    // with the `showSummary`/`showTraceflow` comment above ("entries popping in once loaded reads
    // better than entries vanishing") — a promoted child would otherwise pop in immediately and
    // then jump into the group once loaded resolves, a reflow that comment argues against for the
    // parent item itself.
    function withNestedChildren(
        path: string,
        show: boolean,
        item: React.ReactElement<{ slot?: string }>,
        builtinChildren: { path: string; node: React.ReactNode }[] = []
    ): React.ReactNode {
        const pluginChildren = childrenByParent.get(path) ?? [];
        const renderedPluginChildren = pluginChildren.map((child) => renderPluginNavItem(child, pathname));

        if (!show) {
            if (!loaded) return null;
            // Only the plugin children are promoted. `builtinChildren` are sub-pages of `path`'s
            // own page (Flow List / Service Map), gated off by the very same decision, so keeping
            // them would render links to pages this user cannot open.
            return renderedPluginChildren;
        }
        if (builtinChildren.length === 0 && pluginChildren.length === 0) return item;
        const hasActiveChild =
            builtinChildren.some((child) => pathStartsWith(pathname, child.path)) ||
            pluginChildren.some((child) => pathStartsWith(pathname, child.path));
        return (
            <antrea-nav-group key={`group-${path}`} hasActiveChild={hasActiveChild}>
                {React.cloneElement(item, { slot: 'header' })}
                {builtinChildren.map((child) => child.node)}
                {renderedPluginChildren}
            </antrea-nav-group>
        );
    }

    return (
        <antrea-nav>
            {withNestedChildren('summary', showSummary, (
                <antrea-nav-item {...(pathEquals(pathname, '/summary') || pathname === '/' ? { active: true } : {})}>
                    <Link to="/summary">
                        <DashboardIcon />
                        <span className="nav-label">Summary</span>
                    </Link>
                </antrea-nav-item>
            ))}
            {withNestedChildren('traceflow', showTraceflow, (
                <antrea-nav-item {...(pathStartsWith(pathname, '/traceflow') ? { active: true } : {})}>
                    <Link to="/traceflow">
                        <TraceflowIcon />
                        <span className="nav-label">Traceflow</span>
                    </Link>
                </antrea-nav-item>
            ))}
            {withNestedChildren('flows', showFlows, (
                <antrea-nav-item {...(pathStartsWith(pathname, '/flows') ? { active: true } : {})}>
                    <Link to="/flows/list">
                        <EyeIcon />
                        <span className="nav-label">Flow Visibility</span>
                    </Link>
                </antrea-nav-item>
            ), [
                {
                    path: '/flows/list',
                    node: (
                        <antrea-nav-item key="/flows/list" {...(pathEquals(pathname, '/flows/list') ? { active: true } : {})}>
                            <Link to="/flows/list">
                                <span className="nav-label">Flow List</span>
                            </Link>
                        </antrea-nav-item>
                    ),
                },
                {
                    path: '/flows/map',
                    node: (
                        <antrea-nav-item key="/flows/map" {...(pathEquals(pathname, '/flows/map') ? { active: true } : {})}>
                            <Link to="/flows/map">
                                <span className="nav-label">Service Map</span>
                            </Link>
                        </antrea-nav-item>
                    ),
                },
            ])}
            {withNestedChildren('settings', true, (
                <antrea-nav-item {...(pathEquals(pathname, '/settings') ? { active: true } : {})}>
                    <Link to="/settings">
                        <GearIcon />
                        <span className="nav-label">Settings</span>
                    </Link>
                </antrea-nav-item>
            ))}
            {topLevelPluginEntries.map((entry) => withNestedChildren(
                stripLeadingSlash(entry.path),
                true,
                renderPluginNavItem(entry, pathname)
            ))}
        </antrea-nav>
    );
}
