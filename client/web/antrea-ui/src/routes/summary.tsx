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

import { useState, useEffect, useRef, useMemo } from 'react';
import { CdsCard } from '@cds/react/card';
import { CdsDivider } from '@cds/react/divider';
import { CdsButton } from '@cds/react/button';
import { CdsInput } from '@cds/react/input';
import '@cds/core/icon/register.js';
import React from 'react';
import { AgentInfo, ControllerInfo, Condition, K8sRef, agentInfoAPI, controllerInfoAPI } from '../api/info';
import { FeatureGate, featureGatesAPI } from '../api/featuregates';
import { useAppError} from '../components/errors';
import { WaitForAPIResource } from '../components/progress';
import { SortIcon } from '../components/SortIcon';

type Property = string

type SortConfig = {
    key: Property;
    direction: 'ascending' | 'descending';
};

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
    const itemsPerPage = 10; // Display 10 items per page for Agents table
    const isAgentTable = props.title === 'Agents';

    const [currentPage, setCurrentPage] = useState(1);
    const [sortConfig, setSortConfig] = useState<SortConfig | null>(null);
    const [searchTerm, setSearchTerm] = useState('');

    const propertyNames = props.propertyNames;
    const data = props.data;

    const filteredData = useMemo(() => {
        if (!isAgentTable || !searchTerm) {
            return data;
        }
        // Simple search on the first property (usually Name)
        const searchKeyIndex = 0; // Assuming the first column is the primary identifier (e.g., Name)
        return data.filter(item => {
            const value = props.getProperties(item)[searchKeyIndex];
            return value.toLowerCase().includes(searchTerm.toLowerCase());
        });
    }, [data, searchTerm, isAgentTable, props.getProperties]);

    const sortedData = useMemo(() => {
        if (!sortConfig) {
            return filteredData;
        }
        const dataCopy = [...filteredData];
        const sortKeyIndex = propertyNames.indexOf(sortConfig.key);
        if (sortKeyIndex === -1) return dataCopy;

        dataCopy.sort((a, b) => {
            const aValue = props.getProperties(a)[sortKeyIndex];
            const bValue = props.getProperties(b)[sortKeyIndex];

            // Custom numerical sort for 'Name' column (agent-N)
            if (sortConfig.key === 'Name') {
                const numA = parseInt(aValue.split('-').pop() || '0', 10);
                const numB = parseInt(bValue.split('-').pop() || '0', 10);

                if (!isNaN(numA) && !isNaN(numB)) {
                    const compareResult = numA - numB;
                    return sortConfig.direction === 'ascending' ? compareResult : -compareResult;
                }
            }

            // Custom numerical sort for numeric columns
            if (['Connected Agents', 'Local Pods'].includes(sortConfig.key)) {
                const numA = parseInt(aValue, 10);
                const numB = parseInt(bValue, 10);
                if (!isNaN(numA) && !isNaN(numB)) {
                    return sortConfig.direction === 'ascending' ? numA - numB : numB - numA;
                }
            }

            // Custom date sort for 'Last Heartbeat' column
            if (sortConfig.key === 'Last Heartbeat') {
                const dateA = new Date(aValue).getTime();
                const dateB = new Date(bValue).getTime();
                if (!isNaN(dateA) && !isNaN(dateB)) {
                    return sortConfig.direction === 'ascending' ? dateA - dateB : dateB - dateA;
                }
            }

            // Default string comparison for other columns
            if (aValue < bValue) {
                return sortConfig.direction === 'ascending' ? -1 : 1;
            }
            if (aValue > bValue) {
                return sortConfig.direction === 'ascending' ? 1 : -1;
            }
            return 0;
        });
        return dataCopy;
    }, [filteredData, sortConfig, propertyNames, props.getProperties]);

    const paginatedData = useMemo(() => {
        if (!isAgentTable) {
            return sortedData; // No pagination for non-agent tables
        }
        const startIndex = (currentPage - 1) * itemsPerPage;
        return sortedData.slice(startIndex, startIndex + itemsPerPage);
    }, [sortedData, currentPage, itemsPerPage, isAgentTable]);

    const totalPages = isAgentTable ? Math.ceil(sortedData.length / itemsPerPage) : 1;

    const handleSort = (key: Property) => {
        let direction: 'ascending' | 'descending' = 'ascending';
        if (sortConfig && sortConfig.key === key && sortConfig.direction === 'ascending') {
            direction = 'descending';
        }
        setSortConfig({ key, direction });
        setCurrentPage(1);
    };

    const handleSearchChange = (event: React.ChangeEvent<HTMLInputElement>) => {
        setSearchTerm(event.target.value);
        setCurrentPage(1);
    };

    const handlePageChange = (newPage: number) => {
        setCurrentPage(newPage);
    };

    return (
        <CdsCard title={props.title}>
            <div cds-layout="vertical gap:md">
                <div cds-text="section" cds-layout="p-y:sm">
                    {props.title}
                </div>
                {isAgentTable && (
                    <div cds-layout="p-b:sm">
                        <CdsInput>
                            <label>Search Agents</label>
                            <input
                                type="text"
                                placeholder="Search by name..."
                                value={searchTerm}
                                onChange={handleSearchChange}
                            />
                        </CdsInput>
                    </div>
                )}
                <CdsDivider cds-card-remove-margin></CdsDivider>
                <table cds-table="border:all" cds-text="center body">
                    <thead>
                        <tr>
                            {
                                propertyNames.map(name => (
                                    <th 
                                        key={name} 
                                        onClick={() => handleSort(name)} 
                                        style={{ cursor: 'pointer' }}
                                        className={sortConfig?.key === name ? 'sort-active' : ''}
                                    >
                                        <div cds-layout="horizontal gap:xs align:center">
                                            {name}
                                            <SortIcon 
                                                direction={sortConfig?.key === name ? sortConfig.direction : 'ascending'}
                                                active={sortConfig?.key === name}
                                            />
                                        </div>
                                    </th>
                                ))
                            }
                        </tr>
                    </thead>
                    <tbody>
                        {
                            paginatedData.map((x: T, idx: number) => {
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
                {isAgentTable && totalPages > 1 && (
                    <div cds-layout="horizontal gap:sm align:center p-t:md">
                        <CdsButton size="sm" action="outline" disabled={currentPage === 1} onClick={() => handlePageChange(currentPage - 1)}>Previous</CdsButton>
                        <span cds-text="secondary">Page {currentPage} of {totalPages} ({filteredData.length} items)</span>
                        <CdsButton size="sm" action="outline" disabled={currentPage === totalPages} onClick={() => handlePageChange(currentPage + 1)}>Next</CdsButton>
                    </div>
                )}
            </div>
        </CdsCard>
    );
}

function generateFakeAgents(n: number): AgentInfo[] {
    // Helper function to generate random agent names
    const generateAgentName = () => {
        const prefixes = ['antrea', 'k8s', 'node', 'worker', 'master'];
        const suffixes = ['-agent', '-node', '-vm', '-host', ''];
        const prefix = prefixes[Math.floor(Math.random() * prefixes.length)];
        const suffix = suffixes[Math.floor(Math.random() * suffixes.length)];
        const id = Math.random() < 0.3 ? 
            Math.random().toString(36).substring(2, 6) : // alphanumeric
            Math.floor(Math.random() * 1000).toString(); // numeric
        return `${prefix}${suffix}-${id}`;
    };

    // Helper function to generate random versions
    const generateVersion = () => {
        const major = Math.floor(Math.random() * 3);
        const minor = Math.floor(Math.random() * 10);
        const patch = Math.floor(Math.random() * 20);
        return `v${major}.${minor}.${patch}`;
    };

    // Helper function to generate random timestamps within last 30 days
    const generateTimestamp = () => {
        const now = new Date();
        const thirtyDaysAgo = new Date(now.getTime() - (30 * 24 * 60 * 60 * 1000));
        const randomTime = new Date(
            thirtyDaysAgo.getTime() + Math.random() * (now.getTime() - thirtyDaysAgo.getTime())
        );
        return randomTime.toISOString();
    };

    // Helper function to generate random subnets
    const generateSubnets = () => {
        const count = Math.floor(Math.random() * 3) + 1; // 1-3 subnets
        const subnets = [];
        for (let i = 0; i < count; i++) {
            const ipv4 = `${Math.floor(Math.random() * 255)}.${Math.floor(Math.random() * 255)}.${Math.floor(Math.random() * 255)}.0`;
            const ipv6 = `fd${Math.random().toString(16).substr(2, 2)}:${Math.random().toString(16).substr(2, 4)}::`;
            subnets.push(Math.random() > 0.5 ? `${ipv4}/24` : `${ipv6}/64`);
        }
        return subnets;
    };

    // Helper function to generate OVS version
    const generateOVSVersion = () => {
        const major = Math.floor(Math.random() * 3) + 2; // 2.x.x - 4.x.x
        const minor = Math.floor(Math.random() * 20);
        const patch = Math.floor(Math.random() * 10);
        return `${major}.${minor}.${patch}`;
    };

    return Array.from({ length: n }).map(() => {
        const name = generateAgentName();
        const isHealthy = Math.random() > 0.2; // 80% chance of being healthy
        const lastHeartbeat = generateTimestamp();

        return {
            metadata: { name },
            version: generateVersion(),
            podRef: { 
                name: `antrea-agent-${name}`, 
                namespace: Math.random() > 0.1 ? "kube-system" : "custom-namespace" 
            },
            nodeRef: { 
                name: Math.random() > 0.3 ? name : `node-${Math.floor(Math.random() * 1000)}`
            },
            localPodNum: Math.floor(Math.random() * 100), // 0-99 pods
            nodeSubnets: generateSubnets(),
            ovsInfo: { 
                version: generateOVSVersion(),
                bridgeName: Math.random() > 0.5 ? "br-int" : "custom-bridge",
                flowTable: new Map([["0", Math.floor(Math.random() * 1000)]])
            },
            agentConditions: [
                {
                    type: "AgentHealthy",
                    status: isHealthy ? "True" : "False",
                    lastHeartbeatTime: lastHeartbeat,
                    reason: isHealthy ? "" : "AgentNotReady",
                    message: isHealthy ? "" : "Agent failed health check"
                },
                {
                    type: "TunnelReady",
                    status: Math.random() > 0.1 ? "True" : "False", // 90% chance of tunnel being ready
                    lastHeartbeatTime: lastHeartbeat,
                    reason: "",
                    message: ""
                }
            ]
        };
    });
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

            let finalAgentInfos = agentInfos;
            // If in development mode, generate fake agent data for UI testing
            if (import.meta.env.DEV) {
                console.log('Development environment detected, generating fake agent data...');
                finalAgentInfos = generateFakeAgents(150);
            }

            setControllerInfo(controllerInfo);
            setAgentInfos(finalAgentInfos);

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
                    <ComponentSummary title="Controller Feature Gates" data={controllerFeatureGates!} propertyNames={featureGateProperties} getProperties={featureGatePropertyValues} />
                </WaitForAPIResource>
                <WaitForAPIResource ready={agentFeatureGates !== undefined} text="Loading Agent Feature Gates">
                    <ComponentSummary title="Agent Feature Gates" data={agentFeatureGates!} propertyNames={featureGateProperties} getProperties={featureGatePropertyValues} />
                </WaitForAPIResource>
            </div>
        </main>
    );
}
