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
