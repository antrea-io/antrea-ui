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

package e2e

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/pointer"
)

func createK8sClient(kubeconfigPath string) (*rest.Config, kubernetes.Interface, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	loadingRules.ExplicitPath = kubeconfigPath
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides).ClientConfig()
	if err != nil {
		return nil, nil, err
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, err
	}
	return config, client, err
}

func getAntreaUIPod(ctx context.Context) (*corev1.Pod, error) {
	listOptions := metav1.ListOptions{
		LabelSelector: "app=antrea-ui",
	}
	pods, err := k8sClient.CoreV1().Pods(antreaNamespace).List(ctx, listOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to list Antrea UI Pods: %w", err)
	}
	if len(pods.Items) != 1 {
		return nil, fmt.Errorf("expected *exactly* one Pod")
	}
	return &pods.Items[0], nil
}

var (
	// A DNS-1123 subdomain must consist of lower case alphanumeric characters
	lettersAndDigits = []rune("abcdefghijklmnopqrstuvwxyz0123456789")

	randGen = rand.New(rand.NewSource(time.Now().Unix())) // #nosec G404: random number generator not used for security purposes
)

func randSeq(n int) string {
	b := make([]rune, n)
	for i := range b {
		// #nosec G404: random number generator not used for security purposes
		randIdx := randGen.Intn(len(lettersAndDigits))
		b[i] = lettersAndDigits[randIdx]
	}
	return string(b)
}

// randName generates a DNS-1123 subdomain name
func randName(prefix string) string {
	const nameSuffixLength = 10
	return prefix + randSeq(nameSuffixLength)
}

func createTestNamespace(ctx context.Context) (string, error) {
	name := randName("antrea-ui-e2e-")
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	_, err := k8sClient.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	return name, err
}

func deleteNamespace(ctx context.Context, name string) error {
	return k8sClient.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
}

func createTestDeployment(ctx context.Context, namespace string, name string, numReplicas int32) (*appsv1.Deployment, []corev1.Pod, error) {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels: map[string]string{
				"antrea-ui-e2e": name,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &numReplicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"antrea-ui-e2e": name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"antrea-ui-e2e": name,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "agnhost",
							Image: "registry.k8s.io/e2e-test-images/agnhost:2.39",
						},
					},
					// Set it to 1s for immediate shutdown to reduce test run time and to avoid affecting subsequent tests.
					TerminationGracePeriodSeconds: pointer.Int64(1),
				},
			},
		},
	}
	deployment, err := k8sClient.AppsV1().Deployments(namespace).Create(ctx, deployment, metav1.CreateOptions{})
	if err != nil {
		return nil, nil, err
	}

	if err := wait.PollUntilContextTimeout(ctx, 1*time.Second, 60*time.Second, true, func(ctx context.Context) (bool, error) {
		var err error
		if deployment, err = k8sClient.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{}); err != nil {
			return false, err
		}
		return deployment.Status.AvailableReplicas == numReplicas, nil
	}); err != nil {
		return nil, nil, err
	}

	listOptions := metav1.ListOptions{
		LabelSelector: fmt.Sprintf("antrea-ui-e2e=%s", name),
	}
	pods, err := k8sClient.CoreV1().Pods(namespace).List(ctx, listOptions)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list deployment Pods: %w", err)
	}

	return deployment, pods.Items, nil
}
