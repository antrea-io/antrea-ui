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

package flowstream

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// adminTokenExpiration is how long a minted antrea-ui-admin token is valid for. It only has to
// outlive one flow-stream call: KeepAlive never presents this token again, so nothing depends on
// it surviving for the life of a long-running stream.
const adminTokenExpiration = 10 * time.Minute

// adminTokenRenewBefore is how far ahead of expiry a cached token is treated as stale, so a call
// never races a token that is about to be rejected.
const adminTokenRenewBefore = time.Minute

// AdminTokenSource mints short-lived, self-issued tokens for the antrea-ui-admin ServiceAccount
// via the TokenRequest API, and caches them until they are close to expiry.
//
// It exists because FlowStreamService accepts a bearer token or a client certificate and nothing
// else - no impersonation header - so the admin-password login mode (session.KindImpersonate),
// which normally reaches the API server by impersonating antrea-ui-admin, has no credential it
// can hand to the Flow Aggregator. Minting a real token for that same ServiceAccount gives it one,
// scoped to this one call: every other K8s call made in admin-password mode keeps using
// impersonation.
type AdminTokenSource struct {
	clientset kubernetes.Interface
	namespace string
	saName    string

	// group collapses concurrent minting calls into one CreateToken request instead of one per
	// caller. mutex guards only the cached (token, expiresAt) pair, and is never held across
	// the CreateToken call itself - see Token - so a slow API server serializes at most one
	// in-flight mint, not every stream trying to open one at once.
	group     singleflight.Group
	mutex     sync.Mutex
	token     string
	expiresAt time.Time
}

// NewAdminTokenSource builds an AdminTokenSource that mints tokens for saName in namespace using
// clientset. clientset must authenticate as antrea-ui's own ServiceAccount and be authorized to
// create tokens for saName (verb "create" on resource "serviceaccounts/token", scoped to
// resourceName saName - see build/charts/antrea-ui/templates/role.yaml).
func NewAdminTokenSource(clientset kubernetes.Interface, namespace, saName string) *AdminTokenSource {
	return &AdminTokenSource{
		clientset: clientset,
		namespace: namespace,
		saName:    saName,
	}
}

// Token returns a bearer token for the antrea-ui-admin ServiceAccount, minting (or renewing) one
// if the cached one is missing or close to expiry.
func (a *AdminTokenSource) Token(ctx context.Context) (string, error) {
	if token, ok := a.cached(); ok {
		return token, nil
	}

	// singleflight.Do de-duplicates concurrent callers that all missed the cache onto one
	// CreateToken call; ctx is only used for the caller that actually launches it, which is a
	// standard singleflight caveat (a caller that cancels does not cancel the shared call, and
	// a caller that arrives after it started does not get its own ctx honored either).
	v, err, _ := a.group.Do("token", func() (any, error) {
		// Another caller may have refreshed the cache while we were waiting to become the
		// leader of this singleflight call.
		if token, ok := a.cached(); ok {
			return token, nil
		}
		expirationSeconds := int64(adminTokenExpiration.Seconds())
		tr, err := a.clientset.CoreV1().ServiceAccounts(a.namespace).CreateToken(ctx, a.saName, &authenticationv1.TokenRequest{
			Spec: authenticationv1.TokenRequestSpec{
				ExpirationSeconds: &expirationSeconds,
			},
		}, metav1.CreateOptions{})
		if err != nil {
			return "", fmt.Errorf("failed to mint token for ServiceAccount %s/%s: %w", a.namespace, a.saName, err)
		}

		expiresAt := time.Now().Add(adminTokenExpiration)
		if !tr.Status.ExpirationTimestamp.IsZero() {
			expiresAt = tr.Status.ExpirationTimestamp.Time
		}
		a.mutex.Lock()
		a.token, a.expiresAt = tr.Status.Token, expiresAt
		a.mutex.Unlock()
		return tr.Status.Token, nil
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// cached returns the currently cached token, if it is not close enough to expiry to need
// renewing.
func (a *AdminTokenSource) cached() (string, bool) {
	a.mutex.Lock()
	defer a.mutex.Unlock()
	if a.token == "" || !time.Now().Add(adminTokenRenewBefore).Before(a.expiresAt) {
		return "", false
	}
	return a.token, true
}
