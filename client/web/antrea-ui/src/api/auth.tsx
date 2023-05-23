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
import { encode } from 'base-64';
import { setToken } from './token';
import config from '../config';

const { apiUri } = config;

interface Token {
    tokenType: string
    accessToken: string
    expiresIn: number
}

const api = axios.create({
    baseURL: apiUri,
});

api.defaults.withCredentials = true;

export const authAPI = {
    login: async (username: string, password: string): Promise<Token> => {
        return api.post(`auth/login`, {}, {
            headers: {
                "Authorization": "Basic " + encode(username + ":" + password),
            },
        }).then((response) => response.data as Token).catch(error => handleError(error, "Error when trying to log in"));
    },

    logout: async (): Promise<void> => {
        return api.post(`auth/logout`, {}).then(_ => {}).catch((error) => handleError(error, "Error when trying to log out"));
    },

    refreshToken: async (): Promise<void> => {
        return api.get(`auth/refresh_token`).then((response) => {
            setToken((response.data as Token).accessToken);
        }).catch((error) => {
            if (error.response?.status === 401) {
                // resetting token, this will "redirect" the user to the login screen
                setToken("");
            }
            handleError(error);
        });
    },
};
