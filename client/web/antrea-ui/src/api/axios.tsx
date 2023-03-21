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

import axios, { AxiosError } from 'axios';
import createAuthRefreshInterceptor from 'axios-auth-refresh';
import config from '../config';
import { getToken } from './token';
import { authAPI } from './auth';

const { apiUri } = config;

const api = axios.create({
    baseURL: apiUri,
});

api.interceptors.request.use((request) => {
    request.headers['Authorization'] = `Bearer ${getToken()}`;
    return request;
});

// Function that will be called to refresh authorization
const refreshAuthLogic = (failedRequest: AxiosError) =>
    authAPI.refreshToken().then(() => {
        if (failedRequest.response?.config.headers) {
            // this is not really needed, as the interceptor above will still be called when we
            // retry the original request.
            failedRequest.response.config.headers['Authorization'] = `Bearer ${getToken()}`;
        }
        return Promise.resolve();
    });

// Instantiate the interceptor
createAuthRefreshInterceptor(api, refreshAuthLogic);

export default api;
