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

package k8s

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
// outlive one call: a flow stream's KeepAlive never presents this token again, and support bundle
// collection asks for a token before every request it sends, so nothing depends on it surviving for
// the life of a long-running operation.
const adminTokenExpiration = 10 * time.Minute

// adminTokenRenewBefore is how far ahead of expiry a cached token is treated as stale, so a call
// never races a token that is about to be rejected.
const adminTokenRenewBefore = time.Minute

// adminTokenMintTimeout bounds the CreateToken call singleflight's leader makes. Without it, a
// wedged API server would block the leader - and every follower riding the same call - forever.
const adminTokenMintTimeout = 10 * time.Second

// AdminTokenSource mints short-lived, self-issued tokens for the antrea-ui-admin ServiceAccount
// via the TokenRequest API, for one audience, and caches them until they are close to expiry.
//
// It exists because services other than the API server accept a bearer token or a client
// certificate and nothing else - no impersonation header. The admin-password login mode
// (session.KindImpersonate), which normally reaches the API server by impersonating
// antrea-ui-admin, has no credential it can hand to the Flow Aggregator's FlowStreamService:
// minting a real token for that same ServiceAccount, with the default audience, gives it one,
// scoped to those calls (every K8s call made in admin-password mode keeps using impersonation).
// Support bundle sources reached by URL are always called as antrea-ui-admin, whoever requested
// the bundle, with a token whose only audience is the source's own, which the API server and
// every other source reject.
type AdminTokenSource struct {
	clientset kubernetes.Interface
	namespace string
	saName    string
	// audiences is nil for the API server's default audiences.
	audiences []string

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
// resourceName saName - see build/charts/antrea-ui/templates/role.yaml). audience, when not
// empty, is the only audience of the tokens; empty means the API server's default audiences, so
// that the API server accepts them too.
func NewAdminTokenSource(clientset kubernetes.Interface, namespace, saName, audience string) *AdminTokenSource {
	a := &AdminTokenSource{
		clientset: clientset,
		namespace: namespace,
		saName:    saName,
	}
	if audience != "" {
		a.audiences = []string{audience}
	}
	return a
}

// Token returns a bearer token for the antrea-ui-admin ServiceAccount, minting (or renewing) one
// if the cached one is missing or close to expiry.
func (a *AdminTokenSource) Token(ctx context.Context) (string, error) {
	if token, ok := a.cached(); ok {
		return token, nil
	}

	// singleflight.Do de-duplicates concurrent callers that all missed the cache onto one
	// CreateToken call. The call deliberately does not use any caller's ctx directly: the
	// leader is just whichever caller happened to arrive first, so canceling it (closing that
	// user's tab mid-mint) must not fail every other user's request riding the same call with a
	// spurious "context canceled". context.WithoutCancel keeps the call alive independent of
	// any one caller's lifetime; adminTokenMintTimeout is what actually bounds it, so a wedged
	// API server cannot block the leader (and everyone behind it) forever.
	v, err, _ := a.group.Do("token", func() (any, error) {
		// Another caller may have refreshed the cache while we were waiting to become the
		// leader of this singleflight call.
		if token, ok := a.cached(); ok {
			return token, nil
		}
		mintCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), adminTokenMintTimeout)
		defer cancel()
		expirationSeconds := int64(adminTokenExpiration.Seconds())
		tr, err := a.clientset.CoreV1().ServiceAccounts(a.namespace).CreateToken(mintCtx, a.saName, &authenticationv1.TokenRequest{
			Spec: authenticationv1.TokenRequestSpec{
				Audiences:         a.audiences,
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
