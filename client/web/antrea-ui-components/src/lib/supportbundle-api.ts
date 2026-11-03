// Copyright 2026 Antrea Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import { APIError, getApiBase } from './api.js';

export type SupportBundleStatus = 'Collecting' | 'Collected' | 'Failed';

/** Mirrors apis/v1.SupportBundleSourceStatus. */
export interface SupportBundleSource {
    name: string;
    kind: string;
    status: SupportBundleStatus;
    size?: number;
    error?: string;
}

/** Mirrors apis/v1.SupportBundle. */
export interface SupportBundle {
    id: string;
    status: SupportBundleStatus;
    createdAt: string;
    expiresAt: string;
    createdBy?: string;
    size?: number;
    error?: string;
    sources?: SupportBundleSource[];
}

export function supportBundlePath(id: string): string {
    return `supportbundle/${encodeURIComponent(id)}`;
}

/** A native browser download must reach the API origin, including in local development. */
export function supportBundleDownloadURL(id: string): string {
    return `${getApiBase()}/api/v1/${supportBundlePath(id)}/download`;
}

/** Decode the support bundle API's string and object errors without changing other APIs. */
export function supportBundleErrorMessage(error: unknown): string {
    if (error instanceof APIError) {
        if (error.code === 501) return 'Support bundles are not enabled for this deployment.';
        // Every 429 the backend gives a reason for; only the hourly limiter on the collection
        // route replies with no body.
        if (error.code === 429 && error.message === 'HTTP 429') {
            return 'The shared hourly support bundle collection limit has been reached. ' +
                'Try again later; deleting a bundle does not reset this limit.';
        }
    }
    const message = error instanceof Error ? error.message : String(error);
    try {
        const body: unknown = JSON.parse(message);
        if (typeof body === 'string') return body;
        if (body && typeof body === 'object' && 'error' in body && typeof body.error === 'string') {
            return body.error;
        }
    } catch { /* Non-JSON errors already have a readable message. */ }
    return message;
}
