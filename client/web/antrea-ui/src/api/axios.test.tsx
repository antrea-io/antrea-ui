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

import nock from 'nock';
import api from './axios';
import { setToken, getToken } from './token';
import { APIError } from './common';

vi.mock('./token');

describe('axios instance', () => {
    const token1 = 'token1';
    const token2 = 'token2';
    const getTokenMock = vi.mocked(getToken);
    const setTokenMock = vi.mocked(setToken);

    afterAll(() => {
        vi.restoreAllMocks();
    });
    afterEach(() => {
        vi.resetAllMocks();
    });

    test('valid token', async () => {
        const scope = nock('http://localhost', {
            reqheaders: {
                'Authorization': `Bearer ${token1}`,
            },
        }).get('/api/v1/test').reply(200, 'ok');

        getTokenMock.mockReturnValueOnce(token1);

        await api.get('test');

        // Assert that the expected request was made.
        scope.done();

        expect(getTokenMock).toHaveBeenCalled();
    });

    test('expired token', async () => {
        const scope = nock('http://localhost')
            .get('/api/v1/test').matchHeader('Authorization', `Bearer ${token1}`).reply(401, 'expired token')
            .get('/auth/refresh_token').reply(200, JSON.stringify({
                accessToken: token2,
                tokenType: 'Bearer',
                expiresIn: 3600,
            }))
            .get('/api/v1/test').matchHeader('Authorization', `Bearer ${token2}`).reply(200, 'ok');

        getTokenMock.mockReturnValueOnce(token1).mockReturnValue(token2);

        await api.get('test');

        scope.done();

        // getToken is actually called 3 times:
        //  * once by the request interceptor for the original request (which fails with 401)
        //  * once by the auth refresh interceptor
        //  * once by the request interceptor when retrying the request
        expect(getTokenMock).toHaveBeenCalledTimes(3);
        expect(setTokenMock).toHaveBeenCalledWith(token2);
    });

    test('failed refresh', async () => {
        const scope = nock('http://localhost')
            .get('/api/v1/test').matchHeader('Authorization', `Bearer ${token1}`).reply(401, 'expired token')
            .get('/auth/refresh_token').reply(500, 'unknown error');

        getTokenMock.mockReturnValueOnce(token1);

        await expect(api.get('test')).rejects.toBeInstanceOf(APIError);

        scope.done();

        expect(getTokenMock).toHaveBeenCalled();
        expect(setTokenMock).not.toHaveBeenCalled();
    });

    test('failed refresh with unauthenticated', async () => {
        const scope = nock('http://localhost')
            .get('/api/v1/test').matchHeader('Authorization', `Bearer ${token1}`).reply(401, 'expired token')
            .get('/auth/refresh_token').reply(401, 'expired cookie');

        getTokenMock.mockReturnValueOnce(token1);

        await expect(api.get('test')).rejects.toBeInstanceOf(APIError);

        scope.done();

        expect(getTokenMock).toHaveBeenCalled();
        expect(setTokenMock).toHaveBeenCalledWith("");
    });
});
