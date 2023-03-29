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
