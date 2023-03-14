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

interface ControllerCondition {
    type: string
    status: string
    lastHeartbeatTime: string
    reason: string
    message: string
}

export interface ControllerInfo {
    metadata: {
        name: string
    }
    version: string
    podRef: K8sRef
    nodeRef: K8sRef
    serviceRef: K8sRef
    networkPolicyControllerInfo: NetworkPolicyControllerInfo
    connectedAgentNum: number
    controllerConditions: ControllerCondition[]
    apiPort: number
}

interface OVSInfo {
    version: string
    bridgeName: string
    flowTable: Map<string,number>
}

interface AgentCondition {
    type: string
    status: string
    lastHeartbeatTime: string
    reason: string
    message: string
}

export interface AgentInfo {
    metadata: {
        name: string
    }
    version: string
    podRef: K8sRef
    nodeRef: K8sRef
    nodeSubnets: string[]
    ovsInfo: OVSInfo
    networkPolicyControllerInfo: NetworkPolicyControllerInfo
    localPodNum: number
    agentConditions: AgentCondition[]
    apiPort: number
}

export const controllerInfoAPI = {
    fetch: async (): Promise<ControllerInfo> => {
        return api.get(
            `info/controller`,
        ).then((response) => response.data as ControllerInfo).catch((error) => {
            console.error("Unable to fetch Controller Info");
            handleError(error);
        });
    },
};

export const agentInfoAPI = {
    fetchAll: async (): Promise<AgentInfo[]> => {
        return api.get(
            `info/agents`,
        ).then((response) => response.data as AgentInfo[]).catch((error) => {
            console.error("Unable to fetch Agent Infos");
            handleError(error);
        });
    },

    fetch: async (name: string): Promise<AgentInfo> => {
        return api.get(
            `info/agents/${name}`,
        ).then((response) => response.data as AgentInfo).catch((error) => {
            console.error("Unable to fetch Agent Info");
            handleError(error);
        });
    },
};
