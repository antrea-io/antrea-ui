import { AxiosError } from 'axios';

export class APIError extends Error {
    code: number;
    status: string;
    date: Date;

    constructor(code: number, status: string, ...params: any[]) {
        super(...params);

        // Maintains proper stack trace for where our error was thrown (only available on V8)
        if (Error.captureStackTrace) {
            Error.captureStackTrace(this, APIError);
        }

        this.name = 'APIError';
        this.code = code;
        this.status = status;
        this.date = new Date();
        this.message = `${this.message} (${this.code}, ${this.status})`;
    }
}

export function handleError(error: Error, message?: string) : never {
    if (error instanceof AxiosError) {
        if (error.response) {
            let errorMessage = "Error processing request.";
            if (error.response.data) {
                errorMessage = JSON.stringify(error.response.data);
            } else if (message) {
                errorMessage = message;
            }
            throw new APIError(error.response.status, error.response.statusText, errorMessage);
        }
    }
    throw error;
}
