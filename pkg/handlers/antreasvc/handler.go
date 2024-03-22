// Copyright 2024 Antrea Authors.
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

package antreasvc

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"antrea.io/antrea-ui/pkg/env"
	"antrea.io/antrea-ui/pkg/utils/portforwarder"
)

const antreaSvcName = "antrea"

type requestsHandler struct {
	logger               logr.Logger
	config               *rest.Config
	antreaNamespace      string
	hostMutex            sync.RWMutex
	host                 string
	kubeClient           kubernetes.Interface
	clientProvider       *antreaHTTPClientProvider
	portForwardingNeeded bool
}

func NewRequestsHandler(logger logr.Logger, config *rest.Config, antreaNamespace string) (*requestsHandler, error) {
	antreaSvcAddr := antreaSvcName + "." + antreaNamespace + ".svc"
	host := antreaSvcAddr
	portForwardingNeeded := false
	var antreaSvcConfig *rest.Config
	if !env.IsRunningInPod() {
		logger.Info("Server is not running in Pod, port forwarding is required to access the Antrea Service")
		portForwardingNeeded = true
		host = ""
		antreaSvcConfig = rest.CopyConfig(config)
	}
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	clientProvider := newAntreaClientProvider(logger, antreaSvcConfig, kubeClient, antreaNamespace, antreaSvcAddr)
	return &requestsHandler{
		logger:               logger,
		config:               config,
		antreaNamespace:      antreaNamespace,
		host:                 host,
		kubeClient:           kubeClient,
		clientProvider:       clientProvider,
		portForwardingNeeded: portForwardingNeeded,
	}, nil
}

func (h *requestsHandler) Run(stopCh <-chan struct{}) {
	ctx := wait.ContextForChannel(stopCh)
	go h.clientProvider.Run(ctx)
	if h.portForwardingNeeded {
		go wait.Until(func() {
			service, err := h.kubeClient.CoreV1().Services(h.antreaNamespace).Get(ctx, antreaSvcName, metav1.GetOptions{})
			if err != nil {
				h.logger.Error(err, "Failed to get Antrea Service")
				return
			}
			pods, err := h.kubeClient.CoreV1().Pods(h.antreaNamespace).List(ctx, metav1.ListOptions{
				LabelSelector: labels.SelectorFromSet(service.Spec.Selector).String(),
			})
			if err != nil {
				h.logger.Error(err, "Failed to list Antrea Service Pods")
				return
			}
			if len(pods.Items) == 0 {
				h.logger.Error(err, "No Pod found for Antrea Service")
				return
			}
			// in practice there is a single Pod: the antrea-controller Pod
			pod := pods.Items[0]
			// the antrea-controller Pod has a single container
			apiPort := int(pod.Spec.Containers[0].Ports[0].ContainerPort)
			// use a random local port for listening
			pf, err := portforwarder.NewPortForwarder(h.config, pod.Namespace, pod.Name, apiPort, "localhost", 0)
			if err != nil {
				h.logger.Error(err, "Failed to create port forwarder")
				return
			}
			portCh := make(chan int)
			go func() {
				if err := pf.Run(stopCh, portCh); err != nil {
					h.logger.Error(err, "Port forwarding error")
				}
				close(portCh)
			}()
			for port := range portCh {
				host := fmt.Sprintf("localhost:%d", port)
				h.setHost(host)
				h.logger.Info("Port forwarding is running for Antrea Service", "listenAddr", host)
			}
			h.setHost("")
		}, 5*time.Second, stopCh)
	}
	<-stopCh
}

func (h *requestsHandler) setHost(host string) {
	h.hostMutex.Lock()
	defer h.hostMutex.Unlock()
	h.host = host
}

func (h *requestsHandler) getHost() (string, error) {
	h.hostMutex.RLock()
	defer h.hostMutex.RUnlock()
	if h.host == "" {
		return "", fmt.Errorf("not ready")
	}
	return h.host, nil
}

func (h *requestsHandler) Request(ctx context.Context, method string, path string, body io.Reader) ([]byte, error) {
	host, err := h.getHost()
	if err != nil {
		return nil, err
	}
	client, err := h.clientProvider.GetAntreaClient()
	if err != nil {
		return nil, err
	}
	url := url.URL{
		Scheme: "https",
		Host:   host,
		Path:   path,
	}
	req, err := http.NewRequestWithContext(ctx, method, url.String(), body)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
