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

import { useDispatch } from 'react-redux';
import { setToken } from '../store';
import config from '../config';

const { apiServer } = config;

export function useLogout(): ((msg?: string) => Promise<void>) {
    const dispatch = useDispatch();

    async function logout(msg?: string) {
        dispatch(setToken(""));
        localStorage.removeItem('ui.antrea.io/use-oidc');
        let redirectURL = window.location.origin;
        if (msg) {
            const params = new URLSearchParams();
            params.set('msg', msg);
            redirectURL += `?${params.toString()}`;
        }
        const params = new URLSearchParams();
        params.set('redirect_url', redirectURL);
        window.location.href=`${apiServer}/auth/logout?${params.toString()}`;
    }

    return logout;
}
