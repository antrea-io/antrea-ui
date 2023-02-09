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
        return api.get(`auth/login`, {
            headers: {
                "Authorization": "Basic " + encode(username + ":" + password),
            },
        }).then((response) => response.data as Token).catch(error => handleError(error, "Error when trying to log in"));
    },

    logout: async (): Promise<void> => {
        return api.get(`auth/logout`).then(_ => {}).catch((error) => handleError(error, "Error when trying to log out"));
    },

    refreshToken: async (): Promise<void> => {
        return api.get(`auth/refresh_token`).then((response) => {
            setToken((response.data as Token).accessToken);
        }).catch((error) => {
            if (error.response?.status === 401) {
                // resetting token, this will "redirect" the user to the login screen
                setToken("");
            } else {
                handleError(error);
            }
        });
    },
};
