import React, { useState, useEffect} from 'react';
import { CdsCard } from '@cds/react/card';
import { CdsDivider } from '@cds/react/divider';
import { AgentInfo, ControllerInfo, K8sRef, agentInfoAPI, controllerInfoAPI } from '../api/info';
import { useAPIError} from '../components/errors';
import { WaitForAPIResource } from '../components/progress';

type Property = string

const controllerProperties: Property[] = ["Name", "Version", "Pod Name", "Node Name", "Connected Agents"];
const agentProperties: Property[] = ["Name", "Version", "Pod Name", "Node Name", "Local Pods", "OVS Version"];

function refToString(ref: K8sRef): string {
    if (ref.namespace) return ref.namespace + '/' + ref.name;
    return ref.name;
}

function controllerPropertyValues(controller: ControllerInfo): string[] {
    return [
        controller.metadata.name,
        controller.version,
        refToString(controller.podRef),
        refToString(controller.nodeRef),
        (controller.connectedAgentNum??0).toString(),
    ];
}

function agentPropertyValues(agent: AgentInfo): string[] {
    return [
        agent.metadata.name,
        agent.version,
        refToString(agent.podRef),
        refToString(agent.nodeRef),
        (agent.localPodNum??0).toString(),
        agent.ovsInfo.version,
    ];
}

function ComponentSummary<T>(props: {title: string, data: T[], propertyNames: Property[], getProperties: (x: T) => string[]}) {
    const propertyNames = props.propertyNames;
    const data = props.data;

    return (
        <CdsCard>
            <div cds-layout="vertical gap:md">
                <div cds-text="section" cds-layout="p-y:sm">
                    {props.title}
                </div>
                <CdsDivider cds-card-remove-margin></CdsDivider>
                <table cds-table="border:all" cds-text="center">
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
    const { addError, removeError } = useAPIError();

    useEffect(() => {
        async function getControllerInfo() {
            try {
                const controllerInfo = await controllerInfoAPI.fetch();
                return controllerInfo;
            } catch(e) {
                if (e instanceof Error ) addError(e);
                console.error(e);
            }
        }

        async function getAgentInfos() {
            try {
                const agentInfos = await agentInfoAPI.fetchAll();
                return agentInfos;
            } catch(e) {
                if (e instanceof Error ) addError(e);
                console.error(e);
            }
        }

        // Defining this functions inside of useEffect is recommended
        // https://reactjs.org/docs/hooks-faq.html#is-it-safe-to-omit-functions-from-the-list-of-dependencies
        async function getData() {
            let [controllerInfo, agentInfos] = await Promise.all([getControllerInfo(), getAgentInfos()]);
            setControllerInfo(controllerInfo);
            setAgentInfos(agentInfos);

            if (controllerInfo !== undefined && agentInfos !== undefined) {
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
                <WaitForAPIResource ready={agentInfos !==undefined} text="Loading Agents Information">
                    <ComponentSummary title="Agents" data={agentInfos!} propertyNames={agentProperties} getProperties={agentPropertyValues} />
                </WaitForAPIResource>
            </div>
        </main>
    );
}
