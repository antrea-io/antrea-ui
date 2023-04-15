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

package k8s

import (
	"flag"
	"net/http"
	"os"

	"k8s.io/client-go/dynamic"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	kubeconfigPath string
)

func inCluster() bool {
	_, inCluster := os.LookupEnv("KUBERNETES_SERVICE_HOST")
	return inCluster
}

func restConfig() (*rest.Config, error) {
	var config *rest.Config
	if inCluster() {
		var err error
		if config, err = rest.InClusterConfig(); err != nil {
			return nil, err
		}
	} else {
		loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
		configOverrides := &clientcmd.ConfigOverrides{}
		loadingRules.ExplicitPath = kubeconfigPath
		var err error
		if config, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides).ClientConfig(); err != nil {
			return nil, err
		}
	}
	return config, nil
}

func Client() (*rest.Config, *http.Client, *dynamic.DynamicClient, error) {
	config, err := restConfig()
	if err != nil {
		return nil, nil, nil, err
	}
	httpClient, err := rest.HTTPClientFor(config)
	if err != nil {
		return nil, nil, nil, err
	}
	client, err := dynamic.NewForConfigAndClient(config, httpClient)
	if err != nil {
		return nil, nil, nil, err
	}
	return config, httpClient, client, nil
}

func init() {
	flag.StringVar(&kubeconfigPath, "kubeconfig", "", "absolute path to the Kubeconfig file")
}
