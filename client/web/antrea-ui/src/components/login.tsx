import { useForm, SubmitHandler } from "react-hook-form";
import { ErrorMessage } from '@hookform/error-message';
import { CdsButton } from '@cds/react/button';
import { CdsFormGroup } from '@cds/react/forms';
import { CdsInput } from "@cds/react/input";
import { CdsPassword } from "@cds/react/password";
import { authAPI } from '../api/auth';
import { useAPIError} from './errors';
import { ErrorMessageContainer } from './form-errors';

type Inputs = {
    username: string
    password: string
};

export default function Login(props: { setToken: (token: string) => void }) {
    const { register, handleSubmit, formState: { errors } } = useForm<Inputs>();
    const setToken = props.setToken;
    const { addError } = useAPIError();

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
                    <label>Username</label>
                    <input {...register("username", {
                        required: "Required field",
                    })} defaultValue="admin" />
                </CdsInput>
                <ErrorMessage
                    errors={errors}
                    name="username"
                    as={<ErrorMessageContainer />}
                />
                <CdsPassword>
                    <label>Password</label>
                    <input type="password" {...register("password", {
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
