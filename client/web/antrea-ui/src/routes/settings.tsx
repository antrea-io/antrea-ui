import { useForm, SubmitHandler } from "react-hook-form";
import { CdsCard } from '@cds/react/card';
import { CdsDivider } from '@cds/react/divider';
import { CdsButton } from '@cds/react/button';
import { CdsFormGroup } from '@cds/react/forms';
import { CdsPassword } from "@cds/react/password";
import { ErrorMessage } from '@hookform/error-message';
import { ErrorMessageContainer } from '../components/form-errors';
import { useLogout} from '../components/logout';
import { accountAPI } from '../api/account';
import { useAPIError} from '../components/errors';

type Inputs = {
    currentPassword: string
    newPassword: string
    newPassword2: string
};

function UpdatePassword() {
    const { register, watch, handleSubmit, formState: { errors } } = useForm<Inputs>();

    const [, logout] = useLogout();

    const { addError } = useAPIError();

    const onSubmit: SubmitHandler<Inputs> = async data => {
        try {
            await accountAPI.updatePassword(data.currentPassword, data.newPassword);
        } catch(e) {
            if (e instanceof Error ) addError(e);
            console.error(e);
            return;
        }
        logout();
    };

    return (
        <CdsCard>
            <div cds-layout="vertical gap:md">
                <div cds-text="section" cds-layout="p-y:sm">
                    Update Password
                </div>
                <CdsDivider></CdsDivider>
                <form onSubmit = {handleSubmit(onSubmit)}>
                    <CdsFormGroup layout="horizontal">
                        <CdsPassword>
                            <label>Current Password</label>
                            <input type="password" {...register("currentPassword", {
                                required: "Required field",
                            })} />
                        </CdsPassword>
                        <ErrorMessage
                            errors={errors}
                            name="currentPassword"
                            as={<ErrorMessageContainer />}
                        />
                        <CdsPassword>
                            <label>New Password</label>
                            <input type="password" {...register("newPassword", {
                                required: "Required field",
                            })} />
                        </CdsPassword>
                        <ErrorMessage
                            errors={errors}
                            name="newPassword"
                            as={<ErrorMessageContainer />}
                        />
                        <CdsPassword>
                            <label>Confirm New Password</label>
                            <input type="password" {...register("newPassword2", {
                                required: "Required field",
                                validate: (value: string) => {
                                    if (value !== watch("newPassword")) {
                                        return "Passwords don't match";
                                    }
                                },
                            })} />
                        </CdsPassword>
                        <ErrorMessage
                            errors={errors}
                            name="newPassword2"
                            as={<ErrorMessageContainer />}
                        />
                        <CdsButton type="submit">Submit</CdsButton>
                    </CdsFormGroup>
                </form>
            </div>
        </CdsCard>
    );
}

export default function Settings() {
    return (
        <main>
            <div cds-layout="vertical gap:lg">
                <p cds-text="title">Settings</p>
                <UpdatePassword />
            </div>
        </main>
    );
}
