/**
 * Copyright 2026 Antrea Authors.
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
import { handleError } from './common';

export interface TargetIPLatencyStats {
    targetIP: string
    lastSendTime?: string
    lastRecvTime?: string
    lastMeasuredRTTNanoseconds?: number
}

export interface PeerNodeLatencyStats {
    nodeName: string
    targetIPLatencyStats?: TargetIPLatencyStats[]
}

export interface NodeLatencyStats {
    metadata: {
        name: string
    }
    peerNodeLatencyStats?: PeerNodeLatencyStats[]
}

export const nodeLatencyStatsAPI = {
    fetchAll: async (): Promise<NodeLatencyStats[]> => {
        return api.get<{ items: NodeLatencyStats[] }>(
            `k8s/apis/stats.antrea.io/v1alpha1/nodelatencystats`,
        ).then((response) => response.data.items).catch((error) => {
            console.error("Unable to fetch Node Latency Stats");
            handleError(error);
        });
    },
};
