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
import { handleError } from './common';

export interface K8sRef {
    namespace?: string
    name: string
}

interface NetworkPolicyControllerInfo {
    networkPolicyNum: number
    addressGroupNum: number
    appliedToGroupNum: number
}

export interface Condition {
    type: string
    status: string
    lastHeartbeatTime: string
    reason: string
    message: string
}

export interface ControllerCondition extends Condition { }

export interface ControllerInfo {
    metadata: {
        name: string
    }
    version?: string
    podRef?: K8sRef
    nodeRef?: K8sRef
    serviceRef?: K8sRef
    networkPolicyControllerInfo?: NetworkPolicyControllerInfo
    connectedAgentNum?: number
    controllerConditions?: ControllerCondition[]
    apiPort?: number
}

interface OVSInfo {
    version?: string
    bridgeName?: string
    flowTable?: Map<string,number>
}

export interface AgentCondition extends Condition { }

export interface AgentInfo {
    metadata: {
        name: string
    }
    version?: string
    podRef?: K8sRef
    nodeRef?: K8sRef
    nodeSubnets?: string[]
    ovsInfo?: OVSInfo
    networkPolicyControllerInfo?: NetworkPolicyControllerInfo
    localPodNum?: number
    agentConditions?: AgentCondition[]
    apiPort?: number
}

export const controllerInfoAPI = {
    fetch: async (): Promise<ControllerInfo> => {
        return api.get(
            `k8s/apis/crd.antrea.io/v1beta1/antreacontrollerinfos/antrea-controller`,
        ).then((response) => response.data as ControllerInfo).catch((error) => {
            console.error("Unable to fetch Controller Info");
            handleError(error);
        });
    },
};

export const agentInfoAPI = {
    fetchAll: async (): Promise<AgentInfo[]> => {
        return api.get(
            `k8s/apis/crd.antrea.io/v1beta1/antreaagentinfos`,
        ).then((response) => response.data.items as AgentInfo[]).catch((error) => {
            console.error("Unable to fetch Agent Infos");
            handleError(error);
        });
    },

    fetch: async (name: string): Promise<AgentInfo> => {
        return api.get(
            `k8s/apis/crd.antrea.io/v1beta1/antreaagentinfos/${name}`,
        ).then((response) => response.data as AgentInfo).catch((error) => {
            console.error("Unable to fetch Agent Info");
            handleError(error);
        });
    },
};
