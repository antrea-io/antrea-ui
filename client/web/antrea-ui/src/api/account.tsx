import api from './axios';
import { handleError } from './common';
import { encode } from 'base-64';

export const accountAPI = {
    updatePassword: async (currentPassword: string, newPassword: string): Promise<void> => {
        return api.put(`account/password`, JSON.stringify({
            currentPassword: encode(currentPassword),
            newPassword: encode(newPassword),
        }), {
            headers: {
                "Content-Type": "application/json",
            },
        }).then((response) => {}).catch(handleError);
    },
};
