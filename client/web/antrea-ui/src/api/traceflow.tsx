import api from './axios';
import { APIError, handleError } from './common';
import config from '../config';

const { apiServer } = config;

interface ObjectMetadata {
    creationTimestamp?: string
    name?: string
}

export interface TraceflowPacket {
    ipHeader: {
        protocol?: number
    }
    transportHeader: {
        icmp?: {
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

            for (let i = 0; i < 10; i++) {
                let location = response.headers["location"] ?? "";
                let retryAfter = response.headers["retry-after"] ?? "";
                let waitFor = parseInt(retryAfter) * 1000;
                await new Promise(r => setTimeout(r, waitFor));
                response = await api.get(`${location}`, {
                    baseURL: `${apiServer}`,
                    validateStatus: (status: number) => status === 200 || status === 202,
                });
                if (response.status === 200) {
                    if (withDelete) {
                        await api.delete(`${response.request.responseURL}`, {
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
