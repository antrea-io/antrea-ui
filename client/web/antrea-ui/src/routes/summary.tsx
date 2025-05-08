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
// Note: generateFakeAgents utility in utils/fakeData.ts - only used when VITE_USE_FAKE_AGENTS=true

type Property = string;

type SortConfig = {
    key: Property;
    direction: 'ascending' | 'descending';
};

// Define possible property value types
type PropertyValue = string | number | Date | [string, number];

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

// Define functions that return both displayed value and sort value
type PropertyValuePair = {
    display: string;
    sortValue: PropertyValue;
};

function controllerPropertyValues(controller: ControllerInfo): PropertyValuePair[] {
    const [healthy, lastHeartbeat] = getConditionInfo(controller.controllerConditions, 'ControllerHealthy');
    const connectedAgents = controller.connectedAgentNum ?? 0;
    
    return [
        { display: controller.metadata.name, sortValue: controller.metadata.name },
        { display: controller?.version ?? 'Unknown', sortValue: controller?.version ?? 'Unknown' },
        { display: refToString(controller.podRef), sortValue: refToString(controller.podRef) },
        { display: refToString(controller.nodeRef), sortValue: refToString(controller.nodeRef) },
        { display: connectedAgents.toString(), sortValue: connectedAgents },
        { display: healthy, sortValue: healthy },
        { display: lastHeartbeat, sortValue: new Date(lastHeartbeat) },
    ];
}

function featureGatePropertyValues(featureGate: FeatureGate): PropertyValuePair[] {
    return [
        { display: featureGate.name, sortValue: featureGate.name },
        { display: featureGate.status, sortValue: featureGate.status },
        { display: featureGate.version, sortValue: featureGate.version }
    ];
}

// Helper function to extract node type and numeric suffix for proper sorting
function getNodeNameSortValue(name: string): [string, number] {
    // Match patterns like "k8s-node-worker-123" or "k8s-node-control-plane-1"
    const match = name.match(/^(.*?)(control-plane|worker)-(\d+)$/);
    if (match) {
        // Extract the node type and number
        const prefix = match[1];
        const nodeType = match[2]; // 'control-plane' or 'worker'
        const numericPart = parseInt(match[3], 10);
        
        // Return as tuple with nodeType first (for primary sorting) and number second
        // This ensures control-plane comes before worker when sorting
        return [nodeType, numericPart];
    }
    // Fall back to a default value if pattern doesn't match
    return ['unknown', 0];
}

function agentPropertyValues(agent: AgentInfo): PropertyValuePair[] {
    const [healthy, lastHeartbeat] = getConditionInfo(agent.agentConditions, 'AgentHealthy');
    const localPodNum = agent.localPodNum ?? 0;
    
    return [
        { display: agent.metadata.name, sortValue: getNodeNameSortValue(agent.metadata.name) },
        { display: agent?.version ?? 'Unknown', sortValue: agent?.version ?? 'Unknown' },
        { display: refToString(agent.podRef), sortValue: refToString(agent.podRef) },
        { display: refToString(agent.nodeRef), sortValue: refToString(agent.nodeRef) },
        { display: localPodNum.toString(), sortValue: localPodNum },
        { display: agent.nodeSubnets?.join(',') ?? 'None', sortValue: agent.nodeSubnets?.join(',') ?? 'None' },
        { display: agent?.ovsInfo?.version ?? 'Unknown', sortValue: agent?.ovsInfo?.version ?? 'Unknown' },
        { display: healthy, sortValue: healthy },
        { display: lastHeartbeat, sortValue: new Date(lastHeartbeat) },
    ];
}

interface ComponentSummaryProps<T> {
    title: string;
    data: T[];
    propertyNames: Property[];
    getProperties: (x: T) => PropertyValuePair[];
    sortable?: boolean;
    pageable?: boolean;
    searchable?: boolean;
}

function ComponentSummary<T>(props: ComponentSummaryProps<T>) {
    const { 
        title, 
        data, 
        propertyNames, 
        getProperties, 
        sortable = false, 
        pageable = false, 
        searchable = false 
    } = props;

    const itemsPerPage = 10;
    const [currentPage, setCurrentPage] = useState(1);
    const [sortConfig, setSortConfig] = useState<SortConfig | null>(null);
    const [searchTerm, setSearchTerm] = useState('');

    const filteredData = useMemo(() => {
        if (!searchable || !searchTerm) {
            return data;
        }
        // Simple search on the first property (usually Name)
        const searchKeyIndex = 0; // Assuming the first column is the primary identifier (e.g., Name)
        return data.filter(item => {
            const value = getProperties(item)[searchKeyIndex].display;
            return value.toLowerCase().includes(searchTerm.toLowerCase());
        });
    }, [data, searchTerm, searchable, getProperties]);

    const sortedData = useMemo(() => {
        if (!sortable || !sortConfig) {
            return filteredData;
        }
        const dataCopy = [...filteredData];
        const sortKeyIndex = propertyNames.indexOf(sortConfig.key);
        if (sortKeyIndex === -1) return dataCopy;

        dataCopy.sort((a, b) => {
            const aValue = getProperties(a)[sortKeyIndex].sortValue;
            const bValue = getProperties(b)[sortKeyIndex].sortValue;

            // Handle special case for node name sorting (returns [nodeType, number] tuple)
            if (Array.isArray(aValue) && Array.isArray(bValue) && 
                aValue.length === 2 && bValue.length === 2) {
                // First compare by node type (control-plane vs worker)
                if (aValue[0] !== bValue[0]) {
                    // Sort control-plane before worker
                    const typeComparison = aValue[0].localeCompare(bValue[0]);
                    return sortConfig.direction === 'ascending' ? typeComparison : -typeComparison;
                }
                // If node types are the same, compare by node number
                if (typeof aValue[1] === 'number' && typeof bValue[1] === 'number') {
                    return sortConfig.direction === 'ascending' ? 
                        aValue[1] - bValue[1] : bValue[1] - aValue[1];
                }
            }

            // Type-based sorting for other types
            if (aValue instanceof Date && bValue instanceof Date) {
                // Date sorting
                return sortConfig.direction === 'ascending' 
                    ? aValue.getTime() - bValue.getTime() 
                    : bValue.getTime() - aValue.getTime();
            }
            
            if (typeof aValue === 'number' && typeof bValue === 'number') {
                // Number sorting
                return sortConfig.direction === 'ascending' ? aValue - bValue : bValue - aValue;
            }
            
            // String sorting (default)
            const aString = String(aValue);
            const bString = String(bValue);
            
            if (aString < bString) {
                return sortConfig.direction === 'ascending' ? -1 : 1;
            }
            if (aString > bString) {
                return sortConfig.direction === 'ascending' ? 1 : -1;
            }
            return 0;
        });
        
        return dataCopy;
    }, [filteredData, sortConfig, propertyNames, getProperties, sortable]);

    const paginatedData = useMemo(() => {
        if (!pageable) {
            return sortedData;
        }
        const startIndex = (currentPage - 1) * itemsPerPage;
        return sortedData.slice(startIndex, startIndex + itemsPerPage);
    }, [sortedData, currentPage, itemsPerPage, pageable]);

    const totalPages = pageable ? Math.ceil(sortedData.length / itemsPerPage) : 1;

    const handleSort = (key: Property) => {
        if (!sortable) return;
        
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
        <CdsCard title={title}>
            <div cds-layout="vertical gap:md">
                <div cds-text="section" cds-layout="p-y:sm">
                    {title}
                </div>
                {searchable && (
                    <div cds-layout="p-b:sm">
                        <CdsInput>
                            <label>Search {title}</label>
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
                                        style={{ cursor: sortable ? 'pointer' : 'default' }}
                                        className={sortConfig?.key === name ? 'sort-active' : ''}
                                    >
                                        <div cds-layout="horizontal gap:xs align:center">
                                            {name}
                                            {sortable && (
                                                <SortIcon 
                                                    direction={sortConfig?.key === name ? sortConfig.direction : 'ascending'}
                                                    active={sortConfig?.key === name}
                                                />
                                            )}
                                        </div>
                                    </th>
                                ))
                            }
                        </tr>
                    </thead>
                    <tbody>
                        {
                            paginatedData.map((x: T, idx: number) => {
                                const values = getProperties(x);
                                return (
                                    <tr key={idx}>
                                        {
                                            values.map((v: PropertyValuePair, idx: number) => (
                                                <td key={idx}>{v.display}</td>
                                            ))
                                        }
                                    </tr>
                                );
                            })
                        }
                    </tbody>
                </table>
                {pageable && totalPages > 1 && (
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

        async function getData() {
            const [controllerInfo, agentInfos, featureGates] = await Promise.all([getControllerInfo(), getAgentInfos(), getFeatureGates()]);

            let finalAgentInfos = agentInfos;
            // Use fake agent data if enabled via environment variable
            if (import.meta.env.DEV && import.meta.env.VITE_USE_SYNTHETIC_DATA=== 'true') {
                console.log('Using fake agent data for development...');
                // Dynamic import to avoid including fake data in production bundle
                const { generateFakeAgents } = await import('../utils/fakeData');
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
                    <ComponentSummary 
                        title="Controller" 
                        data={new Array(controllerInfo!)} 
                        propertyNames={controllerProperties} 
                        getProperties={controllerPropertyValues} 
                    />
                </WaitForAPIResource>
                <WaitForAPIResource ready={agentInfos !== undefined} text="Loading Agents Information">
                    <ComponentSummary 
                        title="Agents" 
                        data={agentInfos!} 
                        propertyNames={agentProperties} 
                        getProperties={agentPropertyValues}
                        sortable={true}
                        pageable={true}
                        searchable={true}
                    />
                </WaitForAPIResource>
                <WaitForAPIResource ready={controllerFeatureGates !== undefined} text="Loading Controller Feature Gates">
                    <ComponentSummary 
                        title="Controller Feature Gates" 
                        data={controllerFeatureGates!} 
                        propertyNames={featureGateProperties} 
                        getProperties={featureGatePropertyValues} 
                    />
                </WaitForAPIResource>
                <WaitForAPIResource ready={agentFeatureGates !== undefined} text="Loading Agent Feature Gates">
                    <ComponentSummary 
                        title="Agent Feature Gates" 
                        data={agentFeatureGates!} 
                        propertyNames={featureGateProperties} 
                        getProperties={featureGatePropertyValues} 
                    />
                </WaitForAPIResource>
            </div>
        </main>
    );
}
