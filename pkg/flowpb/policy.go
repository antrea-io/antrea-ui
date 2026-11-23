// Copyright 2026 Antrea Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package flowpb

// Denies reports whether the recorded rule dropped or rejected the connection.
func (a NetworkPolicyRuleAction) Denies() bool {
	return a == NetworkPolicyRuleAction_NETWORK_POLICY_RULE_ACTION_DROP ||
		a == NetworkPolicyRuleAction_NETWORK_POLICY_RULE_ACTION_REJECT
}

// PolicyOutcome combines the recorded actions on both sides of the flow.
func (k *Kubernetes) PolicyOutcome() NetworkPolicyOutcome {
	ingress, egress := k.GetIngressNetworkPolicyRuleAction(), k.GetEgressNetworkPolicyRuleAction()
	switch {
	case ingress.Denies() || egress.Denies():
		return NetworkPolicyOutcome_NETWORK_POLICY_OUTCOME_DENIED
	case ingress == NetworkPolicyRuleAction_NETWORK_POLICY_RULE_ACTION_NO_ACTION && egress == NetworkPolicyRuleAction_NETWORK_POLICY_RULE_ACTION_NO_ACTION:
		return NetworkPolicyOutcome_NETWORK_POLICY_OUTCOME_UNPROTECTED
	default:
		return NetworkPolicyOutcome_NETWORK_POLICY_OUTCOME_ALLOWED
	}
}
