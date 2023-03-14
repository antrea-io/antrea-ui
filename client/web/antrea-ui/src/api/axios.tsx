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
            failedRequest.response.config.headers['Authorization'] = `Bearer ${getToken()}`;
        }
        return Promise.resolve();
    });

// Instantiate the interceptor
createAuthRefreshInterceptor(api, refreshAuthLogic);

export default api;
