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

// supportbundle-source is a support bundle source for the e2e tests (test/e2e/supportbundle_test.go).
// It implements the source protocol described in docs/supportbundle.md twice, with a shared store:
//
//   - under /apis/<group>/<version>, as an APIService, for a plugin-declared apiServer source;
//   - under /api/v1, for an https extra source.
//
// The tarball it returns holds a single identity.json, recording who the request was made as, who
// antrea-ui says requested which bundle, and which way it arrived, so the test can check what
// antrea-ui presented.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

const (
	group   = "supportbundle.e2e.antrea.io"
	version = "v1alpha1"

	// The user extras antrea-ui impersonates antrea-ui-admin with, forwarded by the aggregation
	// layer as X-Remote-Extra-<percent-encoded key> headers.
	extraRequestedBy = "supportbundle.ui.antrea.io/requested-by"
	extraBundleID    = "supportbundle.ui.antrea.io/bundle-id"
)

// Identity is the content of identity.json. test/e2e/supportbundle_test.go has its own copy.
type Identity struct {
	// Via is "apiserver" or "https".
	Via   string `json:"via"`
	User  string `json:"user"`
	Since string `json:"since"`
	// RequestedBy and BundleID are what antrea-ui says about the request: user extras through
	// the apiserver, X-Antrea-UI-* headers over HTTPS.
	RequestedBy string `json:"requestedBy"`
	BundleID    string `json:"bundleID"`
	// AuthorizationHeader is whether the request carried an Authorization header. Through the
	// apiserver it never should: the aggregation layer forwards no credential.
	AuthorizationHeader bool `json:"authorizationHeader"`
}

// caller is what authenticating a request yields.
type caller struct {
	user                string
	requestedBy         string
	bundleID            string
	authorizationHeader bool
}

// authError is a failed authentication (401) or authorization (403).
type authError struct {
	code int
	err  error
}

func (e *authError) Error() string { return e.err.Error() }

func unauthorized(format string, args ...any) error {
	return &authError{code: http.StatusUnauthorized, err: fmt.Errorf(format, args...)}
}

// fail writes err as an error response, with the status code it carries.
func fail(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	var authErr *authError
	if errors.As(err, &authErr) {
		code = authErr.code
	}
	http.Error(w, err.Error(), code)
}

type bundle struct {
	apisv1.SupportBundle
	identity Identity
	polls    int
}

type server struct {
	k8sClient kubernetes.Interface
	// httpsAudience is the audience the tokens presented to the HTTPS endpoint must carry.
	httpsAudience string

	mutex   sync.Mutex
	bundles map[string]*bundle
}

// authenticateAPIServer returns the user the apiserver forwarded the request for, and its
// support bundle extras. Requests from the aggregation layer carry the front-proxy client
// certificate; it is not verified here, since all this fixture needs is to report what it
// received.
func authenticateAPIServer(r *http.Request) (*caller, error) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return nil, unauthorized("no front-proxy client certificate")
	}
	user := r.Header.Get("X-Remote-User")
	if user == "" {
		return nil, unauthorized("no X-Remote-User header")
	}
	c := &caller{user: user, authorizationHeader: r.Header.Get("Authorization") != ""}
	// Header names are canonicalized on the way, so the keys are matched case-insensitively,
	// once percent-decoded.
	for name, values := range r.Header {
		escapedKey, ok := cutPrefixFold(name, "X-Remote-Extra-")
		if !ok || len(values) == 0 {
			continue
		}
		key, err := url.PathUnescape(escapedKey)
		if err != nil {
			continue
		}
		switch {
		case strings.EqualFold(key, extraRequestedBy):
			requestedBy, err := url.PathUnescape(values[0])
			if err != nil {
				return nil, &authError{code: http.StatusBadRequest, err: fmt.Errorf("invalid %s extra: %w", extraRequestedBy, err)}
			}
			c.requestedBy = requestedBy
		case strings.EqualFold(key, extraBundleID):
			c.bundleID = values[0]
		}
	}
	return c, nil
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	return s[len(prefix):], true
}

// authenticateHTTPS resolves the bearer token antrea-ui presented with a TokenReview for the
// expected audience, then checks with a SubjectAccessReview that the reviewed identity may create
// support bundles, as docs/supportbundle.md recommends.
func (s *server) authenticateHTTPS(r *http.Request) (*caller, error) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		return nil, unauthorized("no bearer token")
	}
	review, err := s.k8sClient.AuthenticationV1().TokenReviews().Create(r.Context(), &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{Token: token, Audiences: []string{s.httpsAudience}},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("TokenReview failed: %w", err)
	}
	if !review.Status.Authenticated {
		return nil, unauthorized("token not authenticated: %s", review.Status.Error)
	}
	if !slices.Contains(review.Status.Audiences, s.httpsAudience) {
		return nil, unauthorized("token not valid for audience %s", s.httpsAudience)
	}
	user := review.Status.User
	extra := make(map[string]authorizationv1.ExtraValue, len(user.Extra))
	for k, v := range user.Extra {
		extra[k] = authorizationv1.ExtraValue(v)
	}
	sar, err := s.k8sClient.AuthorizationV1().SubjectAccessReviews().Create(r.Context(), &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:   user.Username,
			UID:    user.UID,
			Groups: user.Groups,
			Extra:  extra,
			ResourceAttributes: &authorizationv1.ResourceAttributes{
				Verb:     "create",
				Group:    "ui.antrea.io",
				Resource: "supportbundles",
			},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("SubjectAccessReview failed: %w", err)
	}
	if !sar.Status.Allowed {
		return nil, &authError{code: http.StatusForbidden, err: fmt.Errorf("%s may not create support bundles", user.Username)}
	}
	requestedBy, err := url.PathUnescape(r.Header.Get("X-Antrea-UI-Requested-By"))
	if err != nil {
		return nil, &authError{code: http.StatusBadRequest, err: fmt.Errorf("invalid X-Antrea-UI-Requested-By header: %w", err)}
	}
	return &caller{
		user:                user.Username,
		requestedBy:         requestedBy,
		bundleID:            r.Header.Get("X-Antrea-UI-Support-Bundle"),
		authorizationHeader: true,
	}, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func tarball(identity Identity) ([]byte, error) {
	data, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "identity.json", Mode: 0644, Size: int64(len(data)), ModTime: time.Now()}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(data); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (s *server) handler(via string, authenticate func(r *http.Request) (*caller, error)) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /supportbundle", func(w http.ResponseWriter, r *http.Request) {
		c, err := authenticate(r)
		if err != nil {
			fail(w, err)
			return
		}
		var request apisv1.SupportBundleRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		now := time.Now()
		b := &bundle{
			SupportBundle: apisv1.SupportBundle{
				ID:        rand.Text(),
				Status:    apisv1.SupportBundleStatusCollecting,
				CreatedBy: c.user,
				CreatedAt: now,
				ExpiresAt: now.Add(time.Hour),
			},
			identity: Identity{
				Via:                 via,
				User:                c.user,
				Since:               request.Since,
				RequestedBy:         c.requestedBy,
				BundleID:            c.bundleID,
				AuthorizationHeader: c.authorizationHeader,
			},
		}
		s.mutex.Lock()
		s.bundles[b.ID] = b
		s.mutex.Unlock()
		log.Printf("Created bundle %s for %s via %s, requested by %q for bundle %q", b.ID, c.user, via, c.requestedBy, c.bundleID)
		w.Header().Set("Retry-After", "1")
		writeJSON(w, http.StatusAccepted, b.SupportBundle)
	})

	// Lists the bundles still held, so the test can check that antrea-ui deleted its own.
	mux.HandleFunc("GET /supportbundle", func(w http.ResponseWriter, r *http.Request) {
		if _, err := authenticate(r); err != nil {
			fail(w, err)
			return
		}
		s.mutex.Lock()
		list := apisv1.SupportBundleList{Items: []apisv1.SupportBundle{}}
		for _, b := range s.bundles {
			list.Items = append(list.Items, b.SupportBundle)
		}
		s.mutex.Unlock()
		writeJSON(w, http.StatusOK, list)
	})

	// The bundle is reported as Collecting on the first poll, so that antrea-ui has to honor
	// Retry-After and poll again.
	mux.HandleFunc("GET /supportbundle/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		if _, err := authenticate(r); err != nil {
			fail(w, err)
			return
		}
		s.mutex.Lock()
		defer s.mutex.Unlock()
		b, ok := s.bundles[r.PathValue("id")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		b.polls++
		if b.polls > 1 {
			b.Status = apisv1.SupportBundleStatusCollected
		} else {
			w.Header().Set("Retry-After", "1")
		}
		writeJSON(w, http.StatusOK, b.SupportBundle)
	})

	mux.HandleFunc("GET /supportbundle/{id}/download", func(w http.ResponseWriter, r *http.Request) {
		if _, err := authenticate(r); err != nil {
			fail(w, err)
			return
		}
		s.mutex.Lock()
		b, ok := s.bundles[r.PathValue("id")]
		var identity Identity
		if ok {
			identity = b.identity
		}
		s.mutex.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		data, err := tarball(identity)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(data)
	})

	mux.HandleFunc("DELETE /supportbundle/{id}", func(w http.ResponseWriter, r *http.Request) {
		if _, err := authenticate(r); err != nil {
			fail(w, err)
			return
		}
		s.mutex.Lock()
		b, ok := s.bundles[r.PathValue("id")]
		if ok {
			delete(s.bundles, b.ID)
		}
		s.mutex.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		log.Printf("Deleted bundle %s", b.ID)
		w.WriteHeader(http.StatusNoContent)
	})

	return mux
}

// discovery serves /apis/<group>/<version>, which the aggregation layer probes to decide whether
// the APIService is available.
func discovery(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, &metav1.APIResourceList{
		TypeMeta:     metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"},
		GroupVersion: group + "/" + version,
		APIResources: []metav1.APIResource{
			{Name: "supportbundle", Kind: "SupportBundle", Verbs: metav1.Verbs{"create", "get", "list", "delete"}},
		},
	})
}

func main() {
	var certFile, keyFile, httpsAudience string
	var port int
	flag.StringVar(&certFile, "tls-cert-file", "/etc/tls/tls.crt", "TLS certificate")
	flag.StringVar(&keyFile, "tls-key-file", "/etc/tls/tls.key", "TLS key")
	flag.IntVar(&port, "port", 8443, "HTTPS port")
	// antrea-ui mints the tokens it presents to an HTTPS source for
	// supportbundle.ui.antrea.io/<source name>: e2e-https is the extra source's name in
	// test/e2e/supportbundle_test.go.
	flag.StringVar(&httpsAudience, "https-audience", "supportbundle.ui.antrea.io/e2e-https", "Audience the tokens presented to the HTTPS endpoint must carry")
	flag.Parse()

	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("Failed to load in-cluster config: %v", err)
	}
	k8sClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Failed to create K8s client: %v", err)
	}
	s := &server{k8sClient: k8sClient, httpsAudience: httpsAudience, bundles: make(map[string]*bundle)}

	apisPrefix := "/apis/" + group + "/" + version
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+apisPrefix, discovery)
	mux.Handle(apisPrefix+"/", http.StripPrefix(apisPrefix, s.handler("apiserver", authenticateAPIServer)))
	mux.Handle("/api/v1/", http.StripPrefix("/api/v1", s.handler("https", s.authenticateHTTPS)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// The aggregation layer presents its front-proxy client certificate when asked.
		TLSConfig: &tls.Config{ClientAuth: tls.RequestClientCert},
	}
	log.Printf("Listening on %s", srv.Addr)
	log.Fatal(srv.ListenAndServeTLS(certFile, keyFile))
}
