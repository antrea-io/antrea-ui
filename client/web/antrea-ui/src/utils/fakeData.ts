import { AgentInfo } from '../api/info';

export function generateFakeAgents(n: number): AgentInfo[] {
    // Create node indices in sequential order
    const nodeIndices = Array.from({ length: n }, (_, i) => i);
    
    // Shuffle the indices to randomize the order of agent names
    const shuffledIndices = [...nodeIndices].sort(() => Math.random() - 0.5);
    
    // Map of all possible node numbers to ensure we generate all nodes (1 to n)
    // but display them in random order
    const generateAgentData = (actualIndex: number, shuffledIndex: number) => {
        // Helper function to generate structured agent names
        const generateAgentName = () => {
            // Use more inclusive terminology (replaced 'master' with 'control-plane')
            const nodeTypes = ['control-plane', 'worker'];
            // First node is control-plane, others are workers
            const nodeType = actualIndex === 0 ? nodeTypes[0] : nodeTypes[1];
            // For worker nodes, use sequential numbering based on actual index
            const nodeNumber = actualIndex === 0 ? 1 : actualIndex;
            return `k8s-node-${nodeType}-${nodeNumber}`;
        };

        // Helper function to generate specific versions (only two possible versions)
        const generateVersion = () => {
            // Use only two possible versions to simulate a cluster during upgrade
            return actualIndex % 3 === 0 ? 'v2.3.0' : 'v2.2.3';
        };

        // Helper function to generate recent timestamps within last 5 minutes
        const generateTimestamp = () => {
            const now = new Date();
            const fiveMinutesAgo = new Date(now.getTime() - (5 * 60 * 1000));
            const randomTime = new Date(
                fiveMinutesAgo.getTime() + Math.random() * (now.getTime() - fiveMinutesAgo.getTime())
            );
            return randomTime.toISOString();
        };

        // Helper function to generate consecutive subnets
        const generateSubnets = () => {
            const count = Math.min(actualIndex % 2 + 1, 2); // 1-2 subnets, more predictable
            const subnets = [];
            
            // Generate sequential IPv4 subnets
            if (count > 0) {
                // Base subnet with sequential third octet
                subnets.push(`10.10.${actualIndex % 255}.0/24`);
            }
            
            // Add IPv6 subnet for some nodes
            if (count > 1) {
                // Sequential IPv6 subnets
                subnets.push(`fd00:10:10:${actualIndex % 100}::/64`);
            }
            
            return subnets;
        };

        // Helper function to generate consistent OVS version
        const generateOVSVersion = () => {
            // Only two possible OVS versions to simulate a cluster during upgrade
            return actualIndex % 4 === 0 ? '3.0.0' : '2.17.3';
        };

        const name = generateAgentName();
        const isHealthy = actualIndex % 5 !== 4; // 80% chance of being healthy (more predictable)
        const lastHeartbeat = generateTimestamp();
        const version = generateVersion();

        return {
            metadata: { name },
            version,
            podRef: { 
                name: `antrea-agent-${name}`, 
                namespace: actualIndex % 10 !== 0 ? "kube-system" : "custom-namespace" 
            },
            nodeRef: { 
                name // Node name matches agent name for consistency
            },
            localPodNum: 10 + (actualIndex * 3) % 90, // More predictable pod distribution
            nodeSubnets: generateSubnets(),
            ovsInfo: { 
                version: generateOVSVersion(),
                bridgeName: actualIndex % 5 === 0 ? "custom-bridge" : "br-int", // Most use br-int
                flowTable: new Map([["0", 100 + (actualIndex * 10) % 900]]) // More predictable flow counts
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
                    status: actualIndex % 10 !== 9 ? "True" : "False", // 90% chance of tunnel being ready
                    lastHeartbeatTime: lastHeartbeat,
                    reason: "",
                    message: ""
                }
            ]
        };
    };

    // Create an array of agents with randomized order but complete node numbering
    return shuffledIndices.map((shuffledIndex, arrayIndex) => {
        // We use the shuffled index as the "actual" node index
        // This ensures we have all nodes from 0 to n-1 but in random order
        return generateAgentData(shuffledIndex, arrayIndex);
    });
}
