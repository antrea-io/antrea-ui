// Copyright 2023 Antrea Authors.
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

import "time"

// Token is the body of the pre-session login/refresh responses. Kept only for
// test/e2e/client.go, which still exercises the old flow until it is rewired onto sessions.
type Token struct {
	TokenType   string `json:"tokenType"`
	AccessToken string `json:"accessToken"`
	ExpiresIn   int64  `json:"expiresIn"`
}

// LoginTokenRequest is the body of POST /auth/login/token: a Kubernetes bearer token (typically a
// ServiceAccount token) that the server will use as the caller's identity.
type LoginTokenRequest struct {
	Token string `json:"token"`
}

// LoginKubeconfigRequest is the body of POST /auth/login/kubeconfig. The kubeconfig is parsed for
// the current context's credential and then discarded; only the credential is retained, in the
// server-side session.
type LoginKubeconfigRequest struct {
	Kubeconfig string `json:"kubeconfig"`
}

// SessionInfo is the body of GET /auth/session. It describes the caller's session; it never
// includes any credential material.
type SessionInfo struct {
	Authenticated bool `json:"authenticated"`
	// Mode is how the user logged in: "oidc", "kubeconfig", "admin" or "serviceAccountToken".
	Mode string `json:"mode,omitempty"`
	// Username is for display only. Authorization is always the API server's decision.
	Username string `json:"username,omitempty"`
	// ExpiresAt is the latest the session can possibly last: the absolute lifetime cap, or the
	// expiry of the credential behind it if that comes first. It is not affected by activity,
	// and an idle session ends well before it (see session.idleTimeout). Absent for a
	// request authenticated with an Authorization header rather than a session.
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}
