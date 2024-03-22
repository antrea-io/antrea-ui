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

import { useState, useEffect} from 'react';
import { CdsCard } from '@cds/react/card';
import { CdsDivider } from '@cds/react/divider';
import { AgentInfo, ControllerInfo, Condition, K8sRef, agentInfoAPI, controllerInfoAPI } from '../api/info';
import { FeatureGate, featureGatesAPI } from '../api/featuregates';
import { useAppError} from '../components/errors';
import { WaitForAPIResource } from '../components/progress';

type Property = string

const controllerProperties: Property[] = ['Name', 'Version', 'Pod Name', 'Node Name', 'Connected Agents', 'Healthy', 'Last Heartbeat'];
const agentProperties: Property[] = ['Name', 'Version', 'Pod Name', 'Node Name', 'Local Pods', 'Node Subnets', 'OVS Version', 'Healthy', 'Last Heartbeat'];
const featureGateProperties: Property[] = ['Name', 'Status', 'Version'];

function refToString(ref: K8sRef | undefined): string {
    if (!ref) return 'Unknown';
    if (ref.namespace) return ref.namespace + '/' + ref.name;
    return ref.name;
}

// returns status and last heartbeat time
function getConditionInfo(conditions: Condition[] | undefined, name: string): [string, string] {
    if (!conditions) return ['False', 'None'];
    const condition = conditions.find(c => c.type === name);
    if (!condition) return ['False', 'None'];
    return [condition.status, new Date(condition.lastHeartbeatTime).toLocaleString()];
}

function controllerPropertyValues(controller: ControllerInfo): string[] {
    const [healthy, lastHeartbeat] = getConditionInfo(controller.controllerConditions, 'ControllerHealthy');
    return [
        controller.metadata.name,
        controller?.version ?? 'Unknown',
        refToString(controller.podRef),
        refToString(controller.nodeRef),
        (controller.connectedAgentNum??0).toString(),
        healthy,
        lastHeartbeat,
    ];
}

function featureGatePropertyValues(featureGate: FeatureGate): string[] {
    return [featureGate.name, featureGate.status, featureGate.version];
}

function agentPropertyValues(agent: AgentInfo): string[] {
    const [healthy, lastHeartbeat] = getConditionInfo(agent.agentConditions, 'AgentHealthy');
    return [
        agent.metadata.name,
        agent?.version ?? 'Unknown',
        refToString(agent.podRef),
        refToString(agent.nodeRef),
        (agent.localPodNum??0).toString(),
        agent.nodeSubnets?.join(',') ?? 'None',
        agent?.ovsInfo?.version ?? 'Unknown',
        healthy,
        lastHeartbeat,
    ];
}

function ComponentSummary<T>(props: {title: string, data: T[], propertyNames: Property[], getProperties: (x: T) => string[]}) {
    const propertyNames = props.propertyNames;
    const data = props.data;

    return (
        <CdsCard title={props.title}>
            <div cds-layout="vertical gap:md">
                <div cds-text="section" cds-layout="p-y:sm">
                    {props.title}
                </div>
                <CdsDivider cds-card-remove-margin></CdsDivider>
                <table cds-table="border:all" cds-text="center body">
                    <thead>
                        <tr>
                            {
                                propertyNames.map(name => (
                                    <th key={name}>{name}</th>
                                ))
                            }
                        </tr>
                    </thead>
                    <tbody>
                        {
                            data.map((x: T, idx: number) => {
                                const values = props.getProperties(x);
                                return (
                                    <tr key={idx}>
                                        {
                                            values.map((v: string, idx: number) => (
                                                <td key={idx}>{v}</td>
                                            ))
                                        }
                                    </tr>
                                );
                            })
                        }
                    </tbody>
                </table>
            </div>
        </CdsCard>
    );
}

export default function Summary() {
    const [controllerInfo, setControllerInfo] = useState<ControllerInfo>();
    const [agentInfos, setAgentInfos] = useState<AgentInfo[]>();
    const [controllerFeatureGates, setControllerFeatureGates] = useState<FeatureGate[]>();
    const [agentFeatureGates, setAgentFeatureGates] = useState<FeatureGate[]>();
    const { addError, removeError } = useAppError();

    useEffect(() => {
        async function getControllerInfo() {
            try {
                const controllerInfo = await controllerInfoAPI.fetch();
                return controllerInfo;
            } catch (e) {
                if (e instanceof Error ) addError(e);
                console.error(e);
            }
        }

        async function getAgentInfos() {
            try {
                const agentInfos = await agentInfoAPI.fetchAll();
                return agentInfos;
            } catch (e) {
                if (e instanceof Error ) addError(e);
                console.error(e);
            }
        }

        async function getFeatureGates() {
            try {
                const featureGates = await featureGatesAPI.fetch();
                return featureGates;
            } catch (e) {
                if (e instanceof Error ) addError(e);
                console.error(e);
            }
        }

        // Defining this functions inside of useEffect is recommended
        // https://reactjs.org/docs/hooks-faq.html#is-it-safe-to-omit-functions-from-the-list-of-dependencies
        async function getData() {
            const [controllerInfo, agentInfos, featureGates] = await Promise.all([getControllerInfo(), getAgentInfos(), getFeatureGates()]);
            setControllerInfo(controllerInfo);
            setAgentInfos(agentInfos);

            if (featureGates !== undefined) {
                setControllerFeatureGates(featureGates.filter((fg) => fg.component === 'controller'));
                setAgentFeatureGates(featureGates.filter((fg) => fg.component === 'agent'));
            }

            if (controllerInfo !== undefined && agentInfos !== undefined && featureGates !== undefined) {
                removeError();
            }
        }

        getData();
    }, [addError, removeError]);

    return (
        <main>
            <div cds-layout="vertical gap:lg">
                <p cds-text="title">Summary</p>
                <WaitForAPIResource ready={controllerInfo !== undefined} text="Loading Controller Information">
                    <ComponentSummary title="Controller" data={new Array(controllerInfo!)} propertyNames={controllerProperties} getProperties={controllerPropertyValues} />
                </WaitForAPIResource>
                <WaitForAPIResource ready={agentInfos !== undefined} text="Loading Agents Information">
                    <ComponentSummary title="Agents" data={agentInfos!} propertyNames={agentProperties} getProperties={agentPropertyValues} />
                </WaitForAPIResource>
                <WaitForAPIResource ready={controllerFeatureGates !== undefined} text="Loading Controller Feature Gates">
                    <ComponentSummary title="Controller Feature Gates" data={controllerFeatureGates} propertyNames={featureGateProperties} getProperties={featureGatePropertyValues} />
                </WaitForAPIResource>
                <WaitForAPIResource ready={agentFeatureGates !== undefined} text="Loading Agent Feature Gates">
                    <ComponentSummary title="Agent Feature Gates" data={agentFeatureGates!} propertyNames={featureGateProperties} getProperties={featureGatePropertyValues} />
                </WaitForAPIResource>
            </div>
        </main>
    );
}
