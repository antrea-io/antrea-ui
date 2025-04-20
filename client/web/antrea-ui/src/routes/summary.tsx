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
import { CdsIcon } from '@cds/react/icon';
import { CdsInput } from '@cds/react/input';
import '@cds/core/icon/register.js';
import React from 'react';
import { AgentInfo, ControllerInfo, Condition, K8sRef, agentInfoAPI, controllerInfoAPI } from '../api/info';
import { FeatureGate, featureGatesAPI } from '../api/featuregates';
import { useAppError} from '../components/errors';
import { WaitForAPIResource } from '../components/progress';

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
    const [sortStatus, setSortStatus] = useState<'idle' | 'sorting' | 'completed'>('idle');
    const [sortingColumn, setSortingColumn] = useState<string | null>(null);
    const sortCompletionTimer = useRef<NodeJS.Timeout | null>(null);

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
        if (!isAgentTable || !sortConfig) {
            return filteredData;
        }
        console.log(`useMemo running for sort: ${sortConfig.key}`);
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
    }, [filteredData, sortConfig, propertyNames, props.getProperties, isAgentTable]);

    // Effect to handle completion and reset
    // Effect 1: Detect sort completion
    useEffect(() => {
        if (sortStatus === 'sorting') {
            console.log('Sort completed, setting status to completed'); // Debug log
            setSortStatus('completed');
        }
    }, [sortedData, sortStatus]);

    // Effect 2: Handle the 'Done!' message timeout
    useEffect(() => {
        // If the status becomes 'completed', start the timer to reset it
        if (sortStatus === 'completed') {
            // Clear any previous timer just in case
            if (sortCompletionTimer.current) {
                clearTimeout(sortCompletionTimer.current);
            }
            // Start the new timer
            sortCompletionTimer.current = setTimeout(() => {
                console.log('Timer finished, resetting status to idle'); // Debug log
                setSortStatus('idle');
                setSortingColumn(null);
                sortCompletionTimer.current = null;
            }, 3000); // 3 seconds
        }

        // Cleanup: Clear the timer if the component unmounts or if status changes away from 'completed'
        return () => {
            if (sortCompletionTimer.current) {
                clearTimeout(sortCompletionTimer.current);
                sortCompletionTimer.current = null;
            }
        };
    // This effect watches sortStatus to trigger the timer when it becomes 'completed'
    }, [sortStatus]);

    const paginatedData = useMemo(() => {
        if (!isAgentTable) {
            return sortedData; // No pagination for non-agent tables
        }
        const startIndex = (currentPage - 1) * itemsPerPage;
        return sortedData.slice(startIndex, startIndex + itemsPerPage);
    }, [sortedData, currentPage, itemsPerPage, isAgentTable]);

    const totalPages = isAgentTable ? Math.ceil(sortedData.length / itemsPerPage) : 1;

    const handleSort = (key: Property) => {
        if (!isAgentTable) return;
        console.log(`handleSort called for: ${key}`);
        let direction: 'ascending' | 'descending' = 'ascending';
        if (sortConfig && sortConfig.key === key && sortConfig.direction === 'ascending') {
            direction = 'descending';
        }
        setSortingColumn(key);
        setSortStatus('sorting');
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
                {isAgentTable && (
                    <div cds-text="caption" style={{ minHeight: '20px', paddingBottom: '5px', textAlign: 'right' }}>
                        {sortStatus === 'sorting' && (
                            <span>Please wait, sorting by {sortingColumn}...</span>
                        )}
                        {sortStatus === 'completed' && (
                            <span style={{ backgroundColor: 'black', padding: '2px 4px', borderRadius: '3px' }}>Done!</span>
                        )}
                        {sortStatus === 'idle' && (
                            <span>Click table headers to sort</span>
                        )}
                    </div>
                )}
                <CdsDivider cds-card-remove-margin></CdsDivider>
                <table cds-table="border:all" cds-text="center body">
                    <thead>
                        <tr>
                            {
                                propertyNames.map(name => (
                                    <th key={name} onClick={() => handleSort(name)} style={{ cursor: isAgentTable ? 'pointer' : 'default' }}>
                                        {name}
                                        {isAgentTable && sortConfig?.key === name && (
                                            <CdsIcon shape="arrow" direction={sortConfig.direction === 'ascending' ? 'up' : 'down'} style={{ marginLeft: '5px' }} />
                                        )}
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
    return Array.from({ length: n }).map((_, i) => ({
      metadata: { name: `agent-${i}` },
      version: "v2.1.0",
      podRef: { name: `antrea-agent-${i}`, namespace: "kube-system" },
      nodeRef: { name: `node-${i}` },
      localPodNum: Math.floor(Math.random() * 10),
      nodeSubnets: [`10.10.${i}.0/24`],
      ovsInfo: { version: "3.0.0" },
      agentConditions: [
        {
          type: "AgentHealthy",
          status: "True",
          lastHeartbeatTime: new Date().toISOString(),
          reason: "",
          message: ""
        }
      ]
    }));
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
