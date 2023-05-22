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

import axios from 'axios';
import { handleError } from './common';
import config from '../config';

const { apiUri } = config;

export interface Settings {
    version: string
    auth: {
        basicEnabled: boolean
        oidcEnabled: boolean
        oidcProviderName?: string
    }
}

const api = axios.create({
    baseURL: apiUri,
});

export const settingsAPI = {
    fetch: async (): Promise<Settings> => {
        return api.get(
            `settings`,
        ).then((response) => response.data as Settings).catch(error => handleError(error, "Error when trying to fetch settings"));
    },
};
