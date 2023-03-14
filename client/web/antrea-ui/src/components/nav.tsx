import React, { useState } from 'react';
import { Link } from "react-router-dom";
import { CdsNavigation, CdsNavigationStart, CdsNavigationItem } from "@cds/react/navigation";
import { CdsIcon } from '@cds/react/icon';
import {
    ClarityIcons,
    // cloudIcon, cloudIconName,
    cogIcon, cogIconName,
    // containerIcon, containerIconName,
    dashboardIcon, dashboardIconName,
    // powerIcon, powerIconName,
    // userIcon, userIconName,
    // eyeIcon, eyeIconName,
    bugIcon, bugIconName,
    // firewallIcon, firewallIconName,
 } from '@cds/core/icon';

ClarityIcons.addIcons(
    // cloudIcon,
    cogIcon,
    // containerIcon,
    dashboardIcon,
    // powerIcon,
    // userIcon,
    // eyeIcon,
    bugIcon,
    // firewallIcon,
);

export default function NavTab() {
    const [navigationOpen, setNavigationOpen] = useState<boolean>(true);

    return (
        <CdsNavigation expanded={navigationOpen}>
            <CdsNavigationStart onClick={() => setNavigationOpen(s => !s)}>Menu</CdsNavigationStart>
            <CdsNavigationItem>
                <Link to="/summary">
                    <CdsIcon shape={dashboardIconName} solid size="sm"></CdsIcon>
                    Summary
                </Link>
            </CdsNavigationItem>
            <CdsNavigationItem>
                <Link to="/traceflow">
                    <CdsIcon shape={bugIconName} solid size="sm"></CdsIcon>
                    Traceflow
                </Link>
            </CdsNavigationItem>
            <CdsNavigationItem>
                <Link to="/settings">
                    <CdsIcon shape={cogIconName} solid size="sm"></CdsIcon>
                    Settings
                </Link>
            </CdsNavigationItem>
        </CdsNavigation>
    );
}
