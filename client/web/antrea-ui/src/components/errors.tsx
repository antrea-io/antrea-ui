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

import React, { useState, useCallback, useContext } from 'react';
import { CdsAlertGroup, CdsAlert } from "@cds/react/alert";

interface APIErrorContextType {
    error: Error | null
    addError: (error: Error) => void
    removeError: () => void
}

export const APIErrorContext = React.createContext<APIErrorContextType>({
    error: null,
    addError: (error: Error) => {},
    removeError: () => {}
});

export function APIErrorProvider(props: React.PropsWithChildren) {
    const [error, setError] = useState<Error | null>(null);

    const removeError = () => setError(null);

    const addError = (error: Error) => setError(error);

    const contextValue = {
        error,
        addError: useCallback((error: Error) => addError(error), []),
        removeError: useCallback(() => removeError(), [])
    };

    return (
        <APIErrorContext.Provider value={contextValue}>
            {props.children}
        </APIErrorContext.Provider>
    );
}

export function useAPIError() {
  const { error, addError, removeError } = useContext(APIErrorContext);
  return { error, addError, removeError };
}

export function APIErrorNotification() {
    const { error, removeError } = useAPIError();

    const handleClose = () => {
        removeError();
    };

    if (!error) {
        return null;
    }

    return (
        <CdsAlertGroup type="banner" status="danger">
            <CdsAlert closable onCloseChange={()=>handleClose()}>{error.message}</CdsAlert>
        </CdsAlertGroup>
    );
}
