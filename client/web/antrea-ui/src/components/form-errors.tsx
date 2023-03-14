import React from 'react';
import { CdsAlertGroup, CdsAlert } from "@cds/react/alert";

export function ErrorMessageContainer(props: React.PropsWithChildren) {
    return (
        <CdsAlertGroup type="banner" status="danger">
            <CdsAlert>{props.children}</CdsAlert>
        </CdsAlertGroup>
    );
}
