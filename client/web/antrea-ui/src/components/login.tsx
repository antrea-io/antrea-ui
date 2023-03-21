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

import { useForm, SubmitHandler } from "react-hook-form";
import { ErrorMessage } from '@hookform/error-message';
import { CdsButton } from '@cds/react/button';
import { CdsFormGroup } from '@cds/react/forms';
import { CdsInput } from "@cds/react/input";
import { CdsPassword } from "@cds/react/password";
import { authAPI } from '../api/auth';
import { useAppError} from './errors';
import { ErrorMessageContainer } from './form-errors';

type Inputs = {
    username: string
    password: string
};

export default function Login(props: { setToken: (token: string) => void }) {
    const { register, handleSubmit, formState: { errors } } = useForm<Inputs>();
    const setToken = props.setToken;
    const { addError } = useAppError();

    const onSubmit: SubmitHandler<Inputs> = async data => {
        try {
            const token = await authAPI.login(data.username, data.password);
            if (token) setToken(token.accessToken);
        } catch(e) {
            if (e instanceof Error ) addError(e);
            console.error(e);
        }
    };

    return (
        <form onSubmit = {handleSubmit(onSubmit)}>
            <CdsFormGroup layout="horizontal">
                <CdsInput>
                    <label htmlFor="username-input">Username</label>
                    <input id="username-input" {...register("username", {
                        required: "Required field",
                    })} defaultValue="admin" />
                </CdsInput>
                <ErrorMessage
                    errors={errors}
                    name="username"
                    as={<ErrorMessageContainer />}
                />
                <CdsPassword>
                    <label htmlFor="password-input">Password</label>
                    <input id="password-input" type="password" {...register("password", {
                        required: "Required field",
                    })} />
                </CdsPassword>
                <ErrorMessage
                    errors={errors}
                    name="password"
                    as={<ErrorMessageContainer />}
                />
                <CdsButton type="submit">Login</CdsButton>
            </CdsFormGroup>
        </form>
    );
}
