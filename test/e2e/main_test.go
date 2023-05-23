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
	"log"
	"os"
	"testing"

	"antrea.io/antrea-ui/test/e2e/utils/portforwarder"
)

func startPortForwarding(ctx context.Context) (string, func(), error) {
	antreaUIPod, err := getAntreaUIPod(ctx)
	if err != nil {
		return "", nil, err
	}
	pf, err := portforwarder.NewPortForwarder(k8sRESTConfig, antreaNamespace, antreaUIPod.Name, antreaUIPort, "localhost", 0)
	if err != nil {
		return "", nil, err
	}
	port, err := pf.Start()
	if err != nil {
		return "", nil, err
	}
	log.Printf("Forwarding port %d to Antrea UI Pod '%s'", port, antreaUIPod.Name)
	host := fmt.Sprintf("localhost:%d", port)
	stop := func() {
		log.Printf("Stop port forwarding to Antrea UI Pod '%s'", antreaUIPod.Name)
		pf.Stop()
	}
	return host, stop, nil
}

// testMain is meant to be called by TestMain and enables the use of defer statements.
func testMain(m *testing.M) int {
	var err error
	k8sRESTConfig, k8sClient, err = createK8sClient("")
	if err != nil {
		log.Fatalf("Error when creating K8s client: %v", err)
	}

	// for API requests, we set up port-forwarding
	// we cannot use the apiserver proxy function, because it strips some HTTP headers from the
	// proxied request, including authentication headers
	// we currently configure port forwarding once for all tests: this could be an issue if the
	// Antrea UI crashes, as forwading would stop working altogether and all subsequent tests
	// would fail
	addr, stopPortForwarding, err := startPortForwarding(context.Background())
	if err != nil {
		log.Fatalf("Failed to configure port-forwarding, make sure the UI is running: %v", err)
	}
	defer stopPortForwarding()
	host = addr

	return m.Run()
}

func TestMain(m *testing.M) {
	os.Exit(testMain(m))
}
