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

import api from './axios';
import { AxiosError } from 'axios';
import { traceflowAPI, Traceflow, TraceflowSpec } from './traceflow';
import { APIError } from './common';
import { v4 as uuidv4 } from 'uuid';

jest.mock('./axios');

function getAxiosError(status: number, statusText: string, message: string): AxiosError {
    return new AxiosError(message, `${status}`, undefined /* config */, undefined /* request */, {
        data: `${message}`,
        status: status,
        statusText: statusText,
    } as any /* response */);
}

describe('Traceflow API', () => {
    const tf = {} as TraceflowSpec;
    const reqId = uuidv4();
    const mock = jest.mocked(api, true);
    const consoleErrorMock = jest.spyOn(console, 'error').mockImplementation(() => {});

    afterAll(() => {
        mock.mockRestore();
        consoleErrorMock.mockRestore();
    });
    afterEach(() => {
        mock.mockReset();
        consoleErrorMock.mockClear();
    });

    test.each<boolean>([true, false])('with delete: %p', async (withDelete: boolean) => {
        mock.post.mockResolvedValueOnce({
            data: {},
            status: 202,
            headers: {
                'location': `/api/v1/traceflow/${reqId}`,
            },
        });

        // note that the call to /status is automatically redirected by the actual axios instance
        // when the status is 302 (Found).
        mock.get.mockResolvedValueOnce({
            data: {
                status: {},
            } as Traceflow,
            status: 200,
            headers: {
                'content-disposition': 'application/json; charset=utf-8',
            },
            request: {
                responseURL: `http://localhost/api/v1/traceflow/${reqId}/result`,
            },
        });

        if (withDelete) {
            mock.delete.mockResolvedValueOnce({
                status: 200,
            });
        } else {
            mock.delete.mockRejectedValueOnce({});
        }

        await traceflowAPI.runTraceflow(tf, withDelete);

        expect(mock.post).toHaveBeenCalledWith(`traceflow`, {spec: tf}, expect.objectContaining({
            headers: {
                'content-type': 'application/json',
            },
        }));
        expect(mock.get).toHaveBeenCalledWith(`/api/v1/traceflow/${reqId}/status`, expect.objectContaining({
            baseURL: '',
        }));
        if (withDelete) {
            // eslint-disable-next-line jest/no-conditional-expect
            expect(mock.delete).toHaveBeenCalledWith(`/api/v1/traceflow/${reqId}`, expect.anything());
        } else {
            // eslint-disable-next-line jest/no-conditional-expect
            expect(mock.delete).not.toHaveBeenCalled();
        }
    });

    test('need to wait for result', async () => {
        mock.post.mockResolvedValueOnce({
            data: {},
            status: 202,
            headers: {
                'location': `/api/v1/traceflow/${reqId}`,
            },
        });

        mock.get.mockResolvedValueOnce({
            status: 200,
            headers: {
                'location': `/api/v1/traceflow/${reqId}/status`,
            },
            request: {
                responseURL: `http://localhost/api/v1/traceflow/${reqId}/status`,
            },
        }).mockResolvedValueOnce({
            data: {
                status: {},
            } as Traceflow,
            status: 200,
            headers: {
                'content-disposition': 'application/json; charset=utf-8',
            },
            request: {
                responseURL: `http://localhost/api/v1/traceflow/${reqId}/result`,
            },
        });

        await traceflowAPI.runTraceflow(tf, false);

        expect(mock.post).toHaveBeenCalledWith(`traceflow`, {spec: tf}, expect.objectContaining({
            headers: {
                'content-type': 'application/json',
            },
        }));
        expect(mock.get).toHaveBeenCalledTimes(2);
        expect(mock.get).toHaveBeenNthCalledWith(1, `/api/v1/traceflow/${reqId}/status`, expect.objectContaining({
            baseURL: '',
        }));
        expect(mock.get).toHaveBeenNthCalledWith(2, `/api/v1/traceflow/${reqId}/status`, expect.objectContaining({
            baseURL: '',
        }));
    });

    test('failed to create', async () => {
        mock.post.mockRejectedValueOnce(getAxiosError(400, 'Bad request', 'Bad Traceflow'));

        await expect(traceflowAPI.runTraceflow(tf, false)).rejects.toBeInstanceOf(APIError);

        expect(mock.post).toHaveBeenCalledWith(`traceflow`, expect.anything(), expect.anything());
        expect(console.error).toHaveBeenCalled();
    });

    test('failed to check status', async () => {
        mock.post.mockResolvedValueOnce({
            data: {},
            status: 202,
            headers: {
                'location': `/api/v1/traceflow/${reqId}/status`,
            },
        });

        mock.get.mockRejectedValueOnce(getAxiosError(500, 'Internal Server Error', 'Failed to check status'));

        await expect(traceflowAPI.runTraceflow(tf, false)).rejects.toBeInstanceOf(APIError);

        expect(mock.post).toHaveBeenCalledWith(`traceflow`, expect.anything(), expect.anything());
        expect(console.error).toHaveBeenCalled();
    });

    test('failed to delete', async () => {
        mock.post.mockResolvedValueOnce({
            data: {},
            status: 202,
            headers: {
                'location': `/api/v1/traceflow/${reqId}/status`,
            },
        });

        mock.get.mockResolvedValueOnce({
            data: {
                status: {},
            } as Traceflow,
            status: 200,
            headers: {
                'content-disposition': 'application/json; charset=utf-8',
            },
            request: {
                responseURL: `http://localhost/api/v1/traceflow/${reqId}/result`,
            },
        });

        mock.delete.mockRejectedValueOnce(getAxiosError(404, 'Not Found', 'Traceflow not found'));

        await expect(traceflowAPI.runTraceflow(tf, true)).resolves.toBeDefined();

        expect(console.error).toHaveBeenCalled();
    });
});
