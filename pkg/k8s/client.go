package k8s

import (
	"flag"
	"os"

	"k8s.io/client-go/dynamic"
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

func DynamicClient() (dynamic.Interface, error) {
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
	return dynamic.NewForConfig(config)
}

func init() {
	flag.StringVar(&kubeconfigPath, "kubeconfig", "", "absolute path to the Kubeconfig file")
}
