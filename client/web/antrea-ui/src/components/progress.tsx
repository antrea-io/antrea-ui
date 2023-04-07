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
