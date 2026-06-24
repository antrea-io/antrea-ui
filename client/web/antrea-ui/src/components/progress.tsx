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

interface WaitForAPIResourceProps {
    ready: boolean
    noCircle?: boolean
    text?: string
}

export function WaitForAPIResource(props: React.PropsWithChildren<WaitForAPIResourceProps>) {
    const { ready, noCircle = false, text = 'Loading' } = props;

    if (!ready) {
        if (noCircle) {
            return <p>{text}</p>;
        }
        return (
            <div className="loading-row">
                <div className="spinner" role="status" aria-label={text} />
                <p style={{ margin: 0 }}>{text}</p>
            </div>
        );
    }

    return <>{props.children}</>;
}
