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

import React, { useState, useCallback, useContext, useRef, useEffect } from 'react';
import '@antrea/ui-components';
import type { AntreaAlert } from '@antrea/ui-components';

interface AppErrorContextType {
    error: Error | null
    addError: (error: Error) => void
    removeError: () => void
}

const AppErrorContext = React.createContext<AppErrorContextType>({
    error: null,
    addError: (_: Error) => {},
    removeError: () => {},
});

export function AppErrorProvider(props: React.PropsWithChildren) {
    const [error, setError] = useState<Error | null>(null);

    const contextValue = {
        error,
        addError: useCallback((error: Error) => setError(error), []),
        removeError: useCallback(() => setError(null), []),
    };

    return (
        <AppErrorContext.Provider value={contextValue}>
            {props.children}
        </AppErrorContext.Provider>
    );
}

// eslint-disable-next-line react-refresh/only-export-components
export function useAppError() {
    const { error, addError, removeError } = useContext(AppErrorContext);
    return { error, addError, removeError };
}

export function AppErrorNotification() {
    const { error, removeError } = useAppError();
    const alertRef = useRef<AntreaAlert>(null);

    // antrea-alert dispatches a native 'antrea-close' CustomEvent, not a React synthetic
    // event — React doesn't map an onX JSX prop to a custom-element event, so this can't be
    // wired via onAntreaClose={...}; it needs a real addEventListener. The alert only exists
    // in the DOM once `error` is set (see the early return below), and refs aren't reactive,
    // so this must depend on `error` too or it'll attach to a ref that's still null.
    useEffect(() => {
        const el = alertRef.current;
        if (!el) return;
        const onClose = () => removeError();
        el.addEventListener('antrea-close', onClose);
        return () => el.removeEventListener('antrea-close', onClose);
    }, [removeError, error]);

    if (!error) return null;

    return (
        <antrea-alert
            ref={alertRef}
            status="danger"
            closable
        >
            {error.message}
        </antrea-alert>
    );
}
