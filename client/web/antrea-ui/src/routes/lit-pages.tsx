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

import { useRef, useEffect, useCallback } from 'react';
import '@antrea/ui-components';
import { useSelector } from 'react-redux';
import { RootState } from '../store';
import { useLogout } from '../components/logout';

function useLitPage() {
    const token = useSelector((state: RootState) => state.token ?? '');
    const ref = useRef<HTMLElement>(null);
    const logout = useLogout();

    const onSessionExpired = useCallback(() => {
        logout('Your session has expired. Please log in again.');
    }, [logout]);

    useEffect(() => {
        const el = ref.current;
        if (!el) return;
        el.addEventListener('antrea-session-expired', onSessionExpired);
        return () => el.removeEventListener('antrea-session-expired', onSessionExpired);
    }, [onSessionExpired]);

    return { ref, token };
}

export function SummaryPage() {
    const { ref, token } = useLitPage();
    return <antrea-summary-page ref={ref} token={token} />;
}

export function TraceflowPage() {
    const { ref, token } = useLitPage();
    return <antrea-traceflow-page ref={ref} token={token} />;
}

export function FlowVisibilityPage() {
    const { ref, token } = useLitPage();
    return <antrea-flow-visibility-page ref={ref} token={token} />;
}

export function SettingsPage() {
    const { ref, token } = useLitPage();
    return <antrea-settings-page ref={ref} token={token} />;
}
