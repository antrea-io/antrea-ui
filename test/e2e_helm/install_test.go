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

package e2e_helm

import (
	"crypto/tls"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/helm"
	http_helper "github.com/gruntwork-io/terratest/modules/http-helper"
	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/gruntwork-io/terratest/modules/random"
)

const (
	antreaNamespace        = "kube-system"
	antreaUIServiceName    = "antrea-ui"
	antreaUIServicePort    = 3000
	antreaUIDeploymentName = "antrea-ui"
)

var (
	helmChartPath     string
	kubeconfigPath    string
	kubeconfigContext string
)

func checkAPIAccess(scheme string, tlsConfig *tls.Config) func(t *testing.T, endpoint string) {
	return func(t *testing.T, endpoint string) {
		url := url.URL{
			Scheme: scheme,
			Host:   endpoint,
			Path:   "api/v1/version",
		}
		http_helper.HttpGetWithRetryWithCustomValidation(
			t,
			url.String(),
			tlsConfig,
			10,            // retries
			1*time.Second, // sleep between retries
			func(statusCode int, body string) bool {
				return statusCode == 200
			},
		)
	}
}

func TestInstall(t *testing.T) {
	// These are Kubeconfig options to configure the K8s Go client
	kubectlOptions := k8s.NewKubectlOptions(kubeconfigPath, kubeconfigContext, antreaNamespace)

	testCases := []struct {
		name          string
		helmSetValues map[string]string
		preInstall    func(t *testing.T, kubectlOptions *k8s.KubectlOptions)
		checks        func(t *testing.T, endpoint string)
	}{
		{
			name:   "default",
			checks: checkAPIAccess("http", nil),
		},
		{
			name: "https - auto",
			helmSetValues: map[string]string{
				"https.enable": "true",
				"https.method": "auto",
			},
			checks: checkAPIAccess(
				"https",
				// #nosec G402: intentional for test
				&tls.Config{
					InsecureSkipVerify: true,
				},
			),
		},
		{
			name: "oidc - secret ref",
			preInstall: func(t *testing.T, kubectlOptions *k8s.KubectlOptions) {
				// Create a Secret with OIDC credentials
				// From https://www.openidconnect.net/ (Auth0)
				// #nosec G101: test credentials
				secretManifest := `
apiVersion: v1
kind: Secret
metadata:
  name: test-oidc-credentials
  namespace: kube-system
stringData:
  clientID: "kbyuFDidLLm280LIwVFiazOqjO3ty8KH"
  clientSecret: "60Op4HFM0I8ajz0WdiStAbziZ-VFQttXuxixHHs2R7r7-CW8GR79l-mmLqMhc-Sa"
`
				k8s.KubectlApplyFromString(t, kubectlOptions, secretManifest)
				t.Cleanup(func() {
					k8s.RunKubectl(t, kubectlOptions, "delete", "secret", "test-oidc-credentials")
				})
			},
			helmSetValues: map[string]string{
				"url":                                  "https://example.com",
				"auth.oidc.enable":                     "true",
				"auth.oidc.issuerURL":                  "https://samples.auth0.com/",
				"auth.oidc.clientIDSecretRef.name":     "test-oidc-credentials",
				"auth.oidc.clientIDSecretRef.key":      "clientID",
				"auth.oidc.clientSecretSecretRef.name": "test-oidc-credentials",
				"auth.oidc.clientSecretSecretRef.key":  "clientSecret",
			},
			checks: checkAPIAccess("http", nil),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			releaseName := fmt.Sprintf("antrea-ui-%s", strings.ToLower(random.UniqueId()))
			helmOptions := &helm.Options{
				KubectlOptions: kubectlOptions,
				SetValues:      tc.helmSetValues,
			}

			// Run pre-install setup if provided
			if tc.preInstall != nil {
				tc.preInstall(t, kubectlOptions)
			}

			// the test will fail immediately in case of error
			helm.Install(t, helmOptions, helmChartPath, releaseName)
			defer helm.Delete(t, helmOptions, releaseName, true)

			// retry at most 60 times, with a 1s delay
			// this is actually a no-op except for LoadBalancer Services
			k8s.WaitUntilServiceAvailable(t, kubectlOptions, antreaUIServiceName, 60, 1*time.Second)
			k8s.WaitUntilDeploymentAvailable(t, kubectlOptions, antreaUIDeploymentName, 60, 1*time.Second)

			// create a tunnel to the Service, so we can access it from the test
			tunnel := k8s.NewTunnel(kubectlOptions, k8s.ResourceTypeService, antreaUIServiceName, 0, antreaUIServicePort)
			defer tunnel.Close()
			tunnel.ForwardPort(t)

			tc.checks(t, tunnel.Endpoint())
		})
	}
}

func TestMain(m *testing.M) {
	flag.StringVar(&helmChartPath, "chart-path", "../../build/charts/antrea-ui", "Path to the antrea-ui chart being tested")
	flag.StringVar(&kubeconfigPath, "kubeconfig", "", "Override default KubeConfig path")
	flag.StringVar(&kubeconfigContext, "context", "", "Override default KubeConfig context")
	flag.Parse()

	os.Exit(m.Run())
}
