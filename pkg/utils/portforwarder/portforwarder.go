// Copyright 2023 Antrea Authors
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

package portforwarder

import (
	"fmt"
	"io"
	"net/http"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

type PortForwarder struct {
	config        *rest.Config
	clientset     kubernetes.Interface
	namespace     string
	name          string
	targetPort    int
	listenAddress string
	listenPort    int
	stopCh        chan struct{}
}

// NewPortForwarder creates Port Forwarder for a Pod / Service
// After creating Port Forwarder object, call Start() on it to start forwarding
// channel and Stop() to terminate it
func NewPortForwarder(
	config *rest.Config,
	namespace string,
	name string,
	targetPort int,
	listenAddress string,
	listenPort int,
) (*PortForwarder, error) {
	pf := &PortForwarder{
		config:        config,
		namespace:     namespace,
		name:          name,
		targetPort:    targetPort,
		listenAddress: listenAddress,
		listenPort:    listenPort,
	}

	var err error
	pf.clientset, err = kubernetes.NewForConfig(pf.config)
	if err != nil {
		return pf, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	return pf, nil
}

// Run is recommended over Start / Stop
func (p *PortForwarder) Run(stopCh <-chan struct{}, portCh chan<- int) error {
	readyCh := make(chan struct{})
	errCh := make(chan error)

	url := p.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(p.namespace).
		Name(p.name).
		SubResource("portforward").URL()

	transport, upgrader, err := spdy.RoundTripperFor(p.config)
	if err != nil {
		return fmt.Errorf("failed to create dialer: %w", err)
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, "POST", url)

	ports := []string{}
	if p.listenPort > 0 {
		ports = append(ports, fmt.Sprintf("%d:%d", p.listenPort, p.targetPort))
	} else {
		ports = append(ports, fmt.Sprintf(":%d", p.targetPort))
	}

	addresses := []string{
		p.listenAddress,
	}

	pf, err := portforward.NewOnAddresses(dialer, addresses, ports, p.stopCh, readyCh, io.Discard, io.Discard)
	if err != nil {
		return fmt.Errorf("port forward request failed: %w", err)
	}

	go func() {
		errCh <- pf.ForwardPorts()
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("port forward request failed: %w", err)
	case <-readyCh:
		ports, err := pf.GetPorts()
		if err != nil {
			return fmt.Errorf("error when getting forwarded ports: %w", err)
		}
		portCh <- int(ports[0].Local)
	case <-stopCh:
		return nil
	}

	select {
	case err := <-errCh:
		return err
	case <-stopCh:
		return nil
	}
}

// Start Port Forwarding channel
func (p *PortForwarder) Start() (int, error) {
	stopCh := make(chan struct{})
	p.stopCh = make(chan struct{})
	portCh := make(chan int)
	errCh := make(chan error)

	go func() {
		errCh <- p.Run(stopCh, portCh)
	}()

	select {
	case err := <-errCh:
		return 0, err
	case port := <-portCh:
		return port, nil
	}
}

// Stop Port Forwarding channel
func (p *PortForwarder) Stop() {
	close(p.stopCh)
}
