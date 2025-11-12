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

import { AxiosError } from 'axios';

interface ErrorConstructorWithStackTrace extends ErrorConstructor {
    captureStackTrace?: (targetObject: object, constructorOpt?: unknown) => void;
}

export class APIError extends Error {
    code: number;
    status: string;
    date: Date;

    constructor(code: number, status: string, ...params: any[]) {
        super(...params);

        // Maintains proper stack trace for where our error was thrown (only available on V8)
        const ErrorWithStackTrace = Error as ErrorConstructorWithStackTrace;
        if (ErrorWithStackTrace.captureStackTrace) {
            ErrorWithStackTrace.captureStackTrace(this, APIError);
        }

        this.name = 'APIError';
        this.code = code;
        this.status = status;
        this.date = new Date();
        this.message = `${this.message} (${this.code}, ${this.status})`;
    }
}

export function handleError(error: Error, message?: string) : never {
    if (error instanceof AxiosError) {
        if (error.response) {
            let errorMessage = "Error processing request.";
            if (error.response.data) {
                errorMessage = JSON.stringify(error.response.data);
            } else if (message) {
                errorMessage = message;
            }
            throw new APIError(error.response.status, error.response.statusText, errorMessage);
        }
    }
    throw error;
}
