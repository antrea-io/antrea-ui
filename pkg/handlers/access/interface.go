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

package access

//go:generate mockgen -source=interface.go -package=testing -destination=testing/mock_interface.go -copyright_file=$MOCKGEN_COPYRIGHT_FILE

// Resolver answers the questions about a user's access that the Kubernetes self-review APIs cannot,
// from a cluster-wide watch on RoleBindings using antrea-ui's own credential.
//
// The main one is which namespaces a user may have access to: SelfSubjectRulesReview answers "what
// may I do in namespace X" but cannot enumerate the X's. Everything here is a hint for rendering, not
// an authorization decision.
type Resolver interface {
	// NamespacesFor returns the matching namespaces, sorted and deduplicated. It returns an
	// error when the RoleBinding cache is unavailable: before the initial sync, or on a cluster
	// where antrea-ui was not granted the RBAC it needs. Callers degrade rather than fail.
	NamespacesFor(username string, groups []string) ([]string, error)

	// ClusterScopeProbeUsable reports whether the namespace antrea-ui evaluates cluster-scoped
	// rules against still holds no RoleBindings. If one ever appears there, rules evaluated
	// against it stop being purely cluster-scoped and the caller must degrade. Returns true when
	// the cache is unavailable and the invariant therefore cannot be checked.
	ClusterScopeProbeUsable() bool
}
