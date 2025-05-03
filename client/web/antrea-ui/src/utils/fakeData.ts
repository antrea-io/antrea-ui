import { AgentInfo } from '../api/info';

export function generateFakeAgents(n: number): AgentInfo[] {
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