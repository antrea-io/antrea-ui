import React from 'react';
import { CdsProgressCircle } from '@cds/react/progress-circle';

interface WaitForAPIResourceProps {
    ready: boolean
    noCircle?: boolean
    text?: string
}

export function WaitForAPIResource(props: React.PropsWithChildren<WaitForAPIResourceProps>) {
    const ready = props.ready;
    const noCircle = props.noCircle || false;
    const text = props.text || "Loading";

    if (!ready) {
        if (noCircle) {
            return <p>{text}</p>;
        } else {
            return (
                <div cds-layout="horizontal gap:md">
                    <CdsProgressCircle size="xl"></CdsProgressCircle>
                    <p>{text}</p>
                </div>
            );
        }
    }
    // Normally, just render children
    return <>{props.children}</>;
}
