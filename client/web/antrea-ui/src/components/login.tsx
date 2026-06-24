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

import { useEffect, useRef } from 'react';
import { useForm, SubmitHandler } from 'react-hook-form';
import '@antrea/ui-components';
import { authAPI } from '../api/auth';
import { Settings } from '../api/settings';
import { useAppError } from './errors';
import config from '../config';

const { apiServer } = config;

type Inputs = {
    username: string
    password: string
};

export function LoginBasic(props: { setToken: (token: string) => void }) {
    const { register, handleSubmit, setValue, formState: { errors } } = useForm<Inputs>({
        defaultValues: { username: '', password: '' },
    });
    const setToken = props.setToken;
    const { addError } = useAppError();

    const usernameRef = useRef<HTMLElement & { value: string }>(null);
    const passwordRef = useRef<HTMLElement & { value: string }>(null);

    const onSubmit: SubmitHandler<Inputs> = async data => {
        try {
            const token = await authAPI.login(data.username, data.password);
            if (token) setToken(token.accessToken);
        } catch(e) {
            if (e instanceof Error) addError(e);
            console.error(e);
        }
    };

    // Register fields for RHF validation rules. We don't attach RHF's returned ref to the
    // web component (shadow DOM breaks the ref-based value read); instead we call setValue()
    // directly from the antrea-input custom event listener below.
    register('username', { required: 'Required field' });
    register('password', { required: 'Required field' });

    useEffect(() => {
        const usernameEl = usernameRef.current;
        const passwordEl = passwordRef.current;
        if (!usernameEl || !passwordEl) return;

        const onUsernameInput = (e: Event) => {
            setValue('username', (e as CustomEvent<{ value: string }>).detail.value, { shouldValidate: false });
        };
        const onPasswordInput = (e: Event) => {
            setValue('password', (e as CustomEvent<{ value: string }>).detail.value, { shouldValidate: false });
        };

        usernameEl.addEventListener('antrea-input', onUsernameInput);
        passwordEl.addEventListener('antrea-input', onPasswordInput);
        return () => {
            usernameEl.removeEventListener('antrea-input', onUsernameInput);
            passwordEl.removeEventListener('antrea-input', onPasswordInput);
        };
    // setValue and register are stable RHF function refs — safe to omit from deps
    // eslint-disable-next-line react-hooks/exhaustive-deps
    }, []);

    return (
        <form className="login-form" onSubmit={handleSubmit(onSubmit)}>
            <antrea-input
                ref={usernameRef}
                label="Username"
                name="username"
                placeholder="admin"
                error={!!errors.username}
                error-message={errors.username?.message}
            />
            <antrea-input
                ref={passwordRef}
                label="Password"
                name="password"
                type="password"
                error={!!errors.password}
                error-message={errors.password?.message}
            />
            <antrea-button type="submit">Login</antrea-button>
        </form>
    );
}

export function LoginOIDC(props: { providerName: string }) {
    function login() {
        const current = window.location.href;
        const params = new URLSearchParams();
        params.set('redirect_url', current);
        window.location.href = `${apiServer}/auth/oauth2/login?${params.toString()}`;
    }

    useEffect(() => {
        if (localStorage.getItem('ui.antrea.io/use-oidc') === 'yes') {
            localStorage.removeItem('ui.antrea.io/use-oidc');
            login();
        }
    }, []);

    return (
        <antrea-button type="button" action="outline" onClick={() => login()}>
            Login with {props.providerName}
        </antrea-button>
    );
}

export default function Login(props: { setToken: (token: string) => void, settings: Settings }) {
    const { settings } = props;

    return (
        <div className="login-form">
            {settings.auth.basicEnabled && <LoginBasic setToken={props.setToken} />}
            {settings.auth.oidcEnabled && (
                <LoginOIDC providerName={settings.auth.oidcProviderName || 'OIDC'} />
            )}
        </div>
    );
}
