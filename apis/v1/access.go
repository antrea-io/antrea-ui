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

package v1

import authorizationv1 "k8s.io/api/authorization/v1"

// AccessSummary tells the frontend what the logged-in user is allowed to do, so it can hide UI it
// would only get a 403 from. It is a hint for rendering, never an authorization decision: every
// request is still authorized by the API server.
type AccessSummary struct {
	// Username and Groups are the identity the API server resolved for this request, as
	// reported by SelfSubjectReview.
	Username string   `json:"username"`
	Groups   []string `json:"groups"`
	// ClusterAdmin is true if the user is allowed every verb on every resource in every API
	// group. Informational; the UI gates on Rules, not on this.
	ClusterAdmin bool `json:"clusterAdmin"`
	// Namespace is what Rules was evaluated for. Empty means cluster scope.
	//
	// omitempty: when Namespace is "", the field is absent from the JSON, not present as "".
	// The frontend type reflects this (namespace?: string).
	Namespace string `json:"namespace,omitempty"`
	// Rules is the SelfSubjectRulesReview result. Rules are additive, so a rule appearing here
	// means the user definitely has that permission; a rule *not* appearing only means "not
	// allowed" when Incomplete is false. EvaluationError is also omitempty upstream, so it too
	// is absent rather than "" when there is no error.
	Rules authorizationv1.SubjectRulesReviewStatus `json:"rules"`
	// Namespaces the user may have access to, or ["*"] if they can list namespaces
	// cluster-wide. A heuristic: Kubernetes has no self-service API for this, so it is derived
	// from RoleBinding subjects. May under-report.
	//
	// Never null, and always a real answer: [] means "subject of no RoleBinding". When the list
	// cannot be worked out at all, the endpoint fails rather than reporting [].
	Namespaces []string `json:"namespaces"`
}
