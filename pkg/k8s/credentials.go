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
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"antrea.io/antrea-ui/pkg/auth/session"
)

// CredentialFromKubeconfig extracts the current context's credential from a kubeconfig.
//
// Only credentials that antrea-ui can present directly are accepted: a bearer token, or a client
// certificate and key. exec credential plugins and auth-provider entries are rejected: they
// describe a program to run or a provider to contact on the *user's* machine, and running them
// server-side would either fail or, worse, execute a user-supplied command inside the antrea-ui
// Pod.
//
// The kubeconfig bytes are the caller's to discard as soon as this returns; nothing here retains
// them, and no part of them is ever included in a returned error.
func CredentialFromKubeconfig(data []byte) (session.Credential, error) {
	fail := func(format string, args ...interface{}) (session.Credential, error) {
		return session.Credential{}, fmt.Errorf(format, args...)
	}

	cfg, err := clientcmd.Load(data)
	if err != nil {
		return fail("kubeconfig is not valid YAML/JSON")
	}
	contextName := cfg.CurrentContext
	if contextName == "" {
		return fail("kubeconfig has no current-context")
	}
	kubeContext, ok := cfg.Contexts[contextName]
	if !ok {
		return fail("kubeconfig has no context named %q", contextName)
	}
	authInfo, ok := cfg.AuthInfos[kubeContext.AuthInfo]
	if !ok {
		return fail("kubeconfig has no user named %q", kubeContext.AuthInfo)
	}

	switch {
	case authInfo.Exec != nil:
		return fail("kubeconfig uses an exec credential plugin, which Antrea UI cannot run on your behalf. " +
			"Run the plugin locally and paste the resulting token instead, or use a different login method")
	case authInfo.AuthProvider != nil:
		return fail("kubeconfig uses an auth-provider (%s), which Antrea UI does not support. "+
			"Use a token or client certificate credential, or a different login method", authInfo.AuthProvider.Name)
	case authInfo.Token != "":
		return BearerCredential([]byte(authInfo.Token))
	case authInfo.TokenFile != "":
		return fail("kubeconfig references a token file, which only exists on your machine. Paste the token itself instead")
	case len(authInfo.ClientCertificateData) > 0 && len(authInfo.ClientKeyData) > 0:
		return certCredential(authInfo.ClientCertificateData, authInfo.ClientKeyData)
	case authInfo.ClientCertificate != "" || authInfo.ClientKey != "":
		return fail("kubeconfig references certificate files, which only exist on your machine. " +
			"Use `kubectl config view --raw --minify` to produce a kubeconfig with the data embedded")
	case authInfo.Username != "":
		return fail("kubeconfig uses HTTP basic authentication, which Antrea UI does not support")
	default:
		return fail("kubeconfig user %q has no usable credential", kubeContext.AuthInfo)
	}
}

// BearerCredential builds a credential from a raw bearer token, deriving its expiry from the JWT
// "exp" claim when the token is a JWT (which projected ServiceAccount tokens and OIDC id_tokens
// are). Legacy, non-expiring ServiceAccount tokens are opaque and get a zero expiry.
func BearerCredential(token []byte) (session.Credential, error) {
	if len(strings.TrimSpace(string(token))) == 0 {
		return session.Credential{}, fmt.Errorf("token is empty")
	}
	return session.Credential{
		Kind:      session.KindBearer,
		Token:     token,
		ExpiresAt: JWTExpiry(token),
	}, nil
}

func certCredential(certPEM, keyPEM []byte) (session.Credential, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return session.Credential{}, fmt.Errorf("client-certificate-data is not valid PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return session.Credential{}, fmt.Errorf("client-certificate-data is not a valid X.509 certificate")
	}
	// Reject an already-expired certificate here rather than letting it fail on the first API
	// call, when the user has no idea why the UI is suddenly broken.
	if time.Now().After(cert.NotAfter) {
		return session.Credential{}, fmt.Errorf("client certificate expired on %s", cert.NotAfter.UTC().Format(time.RFC3339))
	}
	if keyBlock, _ := pem.Decode(keyPEM); keyBlock == nil {
		return session.Credential{}, fmt.Errorf("client-key-data is not valid PEM")
	}
	return session.Credential{
		Kind:      session.KindCert,
		CertPEM:   certPEM,
		KeyPEM:    keyPEM,
		ExpiresAt: cert.NotAfter,
	}, nil
}

// JWTExpiry returns the "exp" claim of a JWT, or the zero time if the token is not a JWT or has no
// "exp". The signature is deliberately NOT verified: the API server is what validates the token,
// and this is only used to decide when to stop trying to use it.
func JWTExpiry(token []byte) time.Time {
	parts := strings.Split(strings.TrimSpace(string(token)), ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}

// ValidateCredential checks that the API server accepts cred, and returns the identity it
// resolves to. Doing this at login means a bad paste or an expired certificate fails on the login
// form instead of turning into a broken first page load.
//
// The returned username is for display only; it is never used for authorization.
//
// Requires Kubernetes >= 1.28, where SelfSubjectReview reached authentication.k8s.io/v1.
func (f *ClientFactory) ValidateCredential(ctx context.Context, cred *session.Credential) (string, error) {
	rt, cleanup, err := f.TransportFor(cred)
	if err != nil {
		return "", err
	}
	if cleanup != nil {
		// This transport is only used for the validation call; the session builds and caches
		// its own.
		defer cleanup()
	}
	clientset, err := kubernetes.NewForConfigAndClient(f.config, f.HTTPClient(rt))
	if err != nil {
		return "", err
	}

	review, err := clientset.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}
	return review.Status.UserInfo.Username, nil
}
