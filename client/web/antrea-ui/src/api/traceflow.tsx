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
import { APIError, handleError } from './common';
import config from '../config';

const { apiServer } = config;

interface ObjectMetadata {
    creationTimestamp?: string
    name?: string
}

export interface TraceflowPacket {
    srcIP?: string
    dstIP?: string
    length?: number
    ipHeader?: {
        srcIP?: string
        protocol?: number
        ttl?: number
        flags?: number
    }
    ipv6Header?: {
        srcIP?: string
        nextHeader?: number
        hopLimit?: number
    }
    transportHeader: {
        icmp?: {
            id?: number
            sequence?: number
        }
        udp?: {
            srcPort?: number
            dstPort?: number
        }
        tcp?: {
            srcPort?: number
            dstPort?: number
            flags?: number
        }
    }
}

export interface TraceflowSpec {
    source: {
        namespace?: string
        pod?: string
        ip?: string
    }
    destination: {
        namespace?: string
        pod?: string
        service?: string
        ip?: string
    }
    packet?: TraceflowPacket
    liveTraffic?: boolean
    droppedOnly?: boolean
    timeout?: number
}

export interface TraceflowObservation {
    component: string
    componentInfo: string
    action: string
    pod: string
    dstMAC: string
    networkPolicy: string
    egress: string
    ttl: number
    translatedSrcIP: string
    translatedDstIP: string
    tunnelDstIP: string
    egressIP: string
}

export interface TraceflowNodeResult {
    node: string
    role: string
    timestamp: number
    observations: TraceflowObservation[]
}

export interface TraceflowStatus {
    phase: string
    reason: string
    startTime: string
    results: TraceflowNodeResult[]
    capturedPacket?: TraceflowPacket
}

interface Traceflow {
    apiVersion?: string
    kind?: string
    metadata?: ObjectMetadata
    spec?: TraceflowSpec
    status?: TraceflowStatus
}

export const traceflowAPI = {
    runTraceflow: async (tf: TraceflowSpec, withDelete: boolean): Promise<TraceflowStatus | undefined> => {
        try {
            let response = await api.post(`traceflow`, {spec: tf}, {
                headers: {
                    "Content-Type": "application/json",
                },
                validateStatus: (status: number) => status === 202,
            });
            let location = response.headers["location"];
            if (!location) {
                throw new APIError(0, "", "Missing Location after creating traceflow request");
            }
            const reqURL = location;
            const statusURL = `${location}/status`;

            for (let i = 0; i < 10; i++) {
                const retryAfter = response.headers["retry-after"] ?? "1";
                const waitFor = parseInt(retryAfter) * 1000;
                await new Promise(r => setTimeout(r, waitFor));
                response = await api.get(`${statusURL}`, {
                    baseURL: `${apiServer}`,
                    validateStatus: (status: number) => status === 200,
                });
                const done = (response.status === 200 && response.request.responseURL.endsWith('/result'));
                if (done) {
                    if (withDelete) {
                        await api.delete(reqURL, {
                            validateStatus: (status: number) => status === 200,
                        }).then(_ => console.log("Traceflow deleted successfully")).catch(_ => console.error("Unable to delete traceflow"));
                    }
                    const tf = response.data as Traceflow;
                    return tf.status;
                }
            }
            throw new APIError(0, "", "Timeout when waiting for traceflow request to complete");
        } catch(err) {
            console.error("Unable to run traceflow");
            handleError(err as Error);
        }
    }
};
