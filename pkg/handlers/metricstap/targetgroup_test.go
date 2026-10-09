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

package metricstap

import (
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	apisv1 "antrea.io/antrea-ui/apis/v1"
)

const testAgentPort = 10350

// agentInfo builds an AntreaAgentInfo the way the dynamic client returns it. A nil caBundle
// leaves the field out.
func agentInfo(node string, port int64, caBundle []byte) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "crd.antrea.io/v1beta1",
		"kind":       "AntreaAgentInfo",
		"metadata":   map[string]any{"name": node},
		"apiPort":    port,
	}}
	if caBundle != nil {
		obj.Object["apiCABundle"] = base64.StdEncoding.EncodeToString(caBundle)
	}
	return obj
}

func nodeWithAddresses(name string, addresses ...corev1.NodeAddress) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.NodeStatus{Addresses: addresses},
	}
}

func internalIP(ip string) corev1.NodeAddress {
	return corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: ip}
}

func externalIP(ip string) corev1.NodeAddress {
	return corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: ip}
}

type agentFixture struct {
	targetGroup   *AgentTargetGroup
	server        *metricsServer
	handler       *expositionHandler
	dynamicClient *dynamicfake.FakeDynamicClient
	kubeClient    *k8sfake.Clientset
}

// newAgentFixture creates the agent target group on top of fake clients holding objects, which are
// AntreaAgentInfos and Nodes, and of an in-memory server standing in for every agent. caBundle
// gives the CA bundle of that server to the function which builds the objects.
//
// The target group is running when the fixture is returned, with what it watches in sync. This must
// be called from a testing/synctest bubble: the watches are backed by fake clients and do no real
// I/O, so the bubble can tell when they have nothing left to do.
func newAgentFixture(t *testing.T, objects func(caBundle []byte) []runtime.Object) *agentFixture {
	t.Helper()
	f := newStoppedAgentFixture(t, objects)
	f.run(t)
	f.sync(t)
	return f
}

// run runs the target group until the end of the test.
func (f *agentFixture) run(t *testing.T) {
	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.targetGroup.Run(stopCh)
	}()
	t.Cleanup(func() {
		close(stopCh)
		<-done
	})
}

// newStoppedAgentFixture is newAgentFixture without running the target group.
func newStoppedAgentFixture(t *testing.T, objects func(caBundle []byte) []runtime.Object) *agentFixture {
	t.Helper()
	f := &agentFixture{handler: &expositionHandler{body: testExposition}}
	f.server = newMetricsServer(t, agentServerName, f.handler)
	var agentInfos, nodes []runtime.Object
	for _, obj := range objects(f.server.caBundle) {
		if _, ok := obj.(*unstructured.Unstructured); ok {
			agentInfos = append(agentInfos, obj)
		} else {
			nodes = append(nodes, obj)
		}
	}
	f.dynamicClient = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{antreaAgentInfoGVR: "AntreaAgentInfoList"}, agentInfos...)
	f.kubeClient = k8sfake.NewSimpleClientset(nodes...)
	scraper := NewScraper(&staticTokenSource{token: testToken}, f.server.dial)
	f.targetGroup = NewAgentTargetGroup(f.dynamicClient, f.kubeClient, scraper)
	return f
}

// sync waits until the target group has seen every change made through the clients so far.
func (f *agentFixture) sync(t *testing.T) {
	t.Helper()
	synctest.Wait()
	require.True(t, f.targetGroup.synced(), "the target group should have synced")
}

// requests counts the requests the target group made to the kube-apiserver.
func (f *agentFixture) requests() int {
	return len(f.dynamicClient.Actions()) + len(f.kubeClient.Actions())
}

func TestAgentTargetGroupTargets(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newAgentFixture(t, func([]byte) []runtime.Object {
			return []runtime.Object{agentInfo("node-b", testAgentPort, nil), agentInfo("node-a", testAgentPort, nil)}
		})
		assert.Equal(t, TargetGroupAgent, f.targetGroup.Name())
		assert.True(t, f.targetGroup.Instanced())

		targets, err := f.targetGroup.Targets(t.Context())
		require.NoError(t, err)
		assert.Equal(t, []apisv1.MetricsTarget{
			{ID: "antrea-agent/node-a", Labels: map[string]string{"node": "node-a"}},
			{ID: "antrea-agent/node-b", Labels: map[string]string{"node": "node-b"}},
		}, targets)
		// Listing does not contact any agent.
		assert.Empty(t, f.server.dialedAddrs())

		// The list follows the agents which come and go.
		_, err = f.dynamicClient.Resource(antreaAgentInfoGVR).Create(t.Context(), agentInfo("node-c", testAgentPort, nil), metav1.CreateOptions{})
		require.NoError(t, err)
		require.NoError(t, f.dynamicClient.Resource(antreaAgentInfoGVR).Delete(t.Context(), "node-a", metav1.DeleteOptions{}))
		f.sync(t)
		targets, err = f.targetGroup.Targets(t.Context())
		require.NoError(t, err)
		assert.Equal(t, []apisv1.MetricsTarget{
			{ID: "antrea-agent/node-b", Labels: map[string]string{"node": "node-b"}},
			{ID: "antrea-agent/node-c", Labels: map[string]string{"node": "node-c"}},
		}, targets)
	})
}

// Until the target group knows the agents and the Nodes of the cluster, it answers nothing: an
// agent which is not known yet must not be reported as one which does not exist.
func TestAgentTargetGroupNotSynced(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newStoppedAgentFixture(t, func(caBundle []byte) []runtime.Object {
			return []runtime.Object{
				agentInfo("node-1", testAgentPort, caBundle),
				nodeWithAddresses("node-1", internalIP("192.0.2.10")),
			}
		})
		_, err := f.targetGroup.Targets(t.Context())
		scrapeErr := requireScrapeError(t, err, ErrorCodeUnavailable)
		assert.Equal(t, "the Antrea agents are not known yet", scrapeErr.Message)
		_, err = f.targetGroup.Scrape(t.Context(), "node-1")
		requireScrapeError(t, err, ErrorCodeUnavailable)
		assert.Empty(t, f.server.dialedAddrs())
	})
}

func TestAgentTargetGroupResolution(t *testing.T) {
	testCases := []struct {
		name string
		// objects builds the AntreaAgentInfo and the Node of node-1.
		objects func(caBundle []byte) []runtime.Object
		// expectedAddr is the address the scrape must connect to.
		expectedAddr string
		// expectedCode is the code of the error, when the agent must not be contacted at all.
		expectedCode string
	}{
		{
			name: "InternalIP is preferred",
			objects: func(caBundle []byte) []runtime.Object {
				return []runtime.Object{
					agentInfo("node-1", testAgentPort, caBundle),
					nodeWithAddresses("node-1", externalIP("203.0.113.7"), internalIP("192.0.2.10"), internalIP("192.0.2.11")),
				}
			},
			expectedAddr: "192.0.2.10:10350",
		},
		{
			name: "ExternalIP without an InternalIP",
			objects: func(caBundle []byte) []runtime.Object {
				return []runtime.Object{
					agentInfo("node-1", testAgentPort, caBundle),
					nodeWithAddresses("node-1", corev1.NodeAddress{Type: corev1.NodeHostName, Address: "node-1"}, externalIP("203.0.113.7")),
				}
			},
			expectedAddr: "203.0.113.7:10350",
		},
		{
			name: "IPv6 and the port the agent reports",
			objects: func(caBundle []byte) []runtime.Object {
				return []runtime.Object{
					agentInfo("node-1", 10360, caBundle),
					nodeWithAddresses("node-1", internalIP("2001:db8::10")),
				}
			},
			expectedAddr: "[2001:db8::10]:10360",
		},
		{
			name: "no agent on the Node",
			objects: func([]byte) []runtime.Object {
				return []runtime.Object{nodeWithAddresses("node-1", internalIP("192.0.2.10"))}
			},
			expectedCode: ErrorCodeNotFound,
		},
		{
			name: "no Node",
			objects: func(caBundle []byte) []runtime.Object {
				return []runtime.Object{agentInfo("node-1", testAgentPort, caBundle)}
			},
			expectedCode: ErrorCodeNotFound,
		},
		{
			name: "no CA bundle",
			objects: func([]byte) []runtime.Object {
				return []runtime.Object{
					agentInfo("node-1", testAgentPort, nil),
					nodeWithAddresses("node-1", internalIP("192.0.2.10")),
				}
			},
			expectedCode: ErrorCodeInvalidTarget,
		},
		{
			name: "CA bundle which is not base64",
			objects: func([]byte) []runtime.Object {
				info := agentInfo("node-1", testAgentPort, nil)
				info.Object["apiCABundle"] = "-----BEGIN CERTIFICATE-----"
				return []runtime.Object{info, nodeWithAddresses("node-1", internalIP("192.0.2.10"))}
			},
			expectedCode: ErrorCodeInvalidTarget,
		},
		{
			name: "no API port",
			objects: func(caBundle []byte) []runtime.Object {
				return []runtime.Object{
					agentInfo("node-1", 0, caBundle),
					nodeWithAddresses("node-1", internalIP("192.0.2.10")),
				}
			},
			expectedCode: ErrorCodeInvalidTarget,
		},
		{
			name: "no address",
			objects: func(caBundle []byte) []runtime.Object {
				return []runtime.Object{
					agentInfo("node-1", testAgentPort, caBundle),
					nodeWithAddresses("node-1", corev1.NodeAddress{Type: corev1.NodeHostName, Address: "node-1"}),
				}
			},
			expectedCode: ErrorCodeInvalidTarget,
		},
		{
			name: "address which is not an IP",
			objects: func(caBundle []byte) []runtime.Object {
				return []runtime.Object{
					agentInfo("node-1", testAgentPort, caBundle),
					nodeWithAddresses("node-1", internalIP("kubernetes.default.svc")),
				}
			},
			expectedCode: ErrorCodeInvalidTarget,
		},
	}
	// An address which is refused is not skipped in favor of the next one: the ExternalIP here
	// is never used.
	for name, ip := range map[string]string{
		"loopback":               "127.0.0.1",
		"IPv6 loopback":          "::1",
		"IPv4-mapped loopback":   "::ffff:127.0.0.1",
		"link-local":             "169.254.169.254",
		"IPv6 link-local":        "fe80::1",
		"unspecified":            "0.0.0.0",
		"IPv6 unspecified":       "::",
		"link-local (multicast)": "224.0.0.1",
	} {
		testCases = append(testCases, struct {
			name         string
			objects      func(caBundle []byte) []runtime.Object
			expectedAddr string
			expectedCode string
		}{
			name: "refused address: " + name,
			objects: func(caBundle []byte) []runtime.Object {
				return []runtime.Object{
					agentInfo("node-1", testAgentPort, caBundle),
					nodeWithAddresses("node-1", internalIP(ip), externalIP("203.0.113.7")),
				}
			},
			expectedCode: ErrorCodeInvalidTarget,
		})
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newAgentFixture(t, tc.objects)
				families, err := f.targetGroup.Scrape(t.Context(), "node-1")
				if tc.expectedCode != "" {
					scrapeErr := requireScrapeError(t, err, tc.expectedCode)
					assert.NotContains(t, scrapeErr.Message, "127.0.0.1")
					assert.Empty(t, f.server.dialedAddrs(), "the agent must not be contacted")
					return
				}
				require.NoError(t, err)
				assert.Len(t, families, 2)
				assert.Equal(t, []string{tc.expectedAddr}, f.server.dialedAddrs())
				requests := f.handler.received()
				require.Len(t, requests, 1)
				// The certificate of an agent is issued for localhost, whatever the address.
				assert.Equal(t, agentServerName, requests[0].TLS.ServerName)
				assert.Equal(t, "Bearer "+testToken, requests[0].Header.Get("Authorization"))
			})
		})
	}
}

// The target group needs both the agents and the Nodes: with only one of the two, an agent which
// exists would be reported as one which does not.
func TestAgentTargetGroupPartlySynced(t *testing.T) {
	forbidden := func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{}, "", errors.New("RBAC: not allowed"))
	}
	testCases := []struct {
		name string
		// fail makes one of the two lists of the target group fail.
		fail func(f *agentFixture)
	}{
		{"without the AntreaAgentInfos", func(f *agentFixture) { f.dynamicClient.PrependReactor("list", "antreaagentinfos", forbidden) }},
		{"without the Nodes", func(f *agentFixture) { f.kubeClient.PrependReactor("list", "nodes", forbidden) }},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newStoppedAgentFixture(t, func(caBundle []byte) []runtime.Object {
					return []runtime.Object{
						agentInfo("node-1", testAgentPort, caBundle),
						nodeWithAddresses("node-1", internalIP("192.0.2.10")),
					}
				})
				tc.fail(f)
				f.run(t)
				synctest.Wait()
				require.NotEqual(t, f.targetGroup.agentInfos.HasSynced(), f.targetGroup.nodes.HasSynced(), "one of the two should have synced")

				_, err := f.targetGroup.Targets(t.Context())
				requireScrapeError(t, err, ErrorCodeUnavailable)
				_, err = f.targetGroup.Scrape(t.Context(), "node-1")
				requireScrapeError(t, err, ErrorCodeUnavailable)
				assert.Empty(t, f.server.dialedAddrs())
			})
		})
	}
}

// What a scrape costs the kube-apiserver does not depend on how many agents are scraped, how often,
// or under which names: the target group asks for nothing once it watches.
func TestAgentTargetGroupScrapesCostNoRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newAgentFixture(t, func(caBundle []byte) []runtime.Object {
			return []runtime.Object{
				agentInfo("node-1", testAgentPort, caBundle),
				nodeWithAddresses("node-1", internalIP("192.0.2.10")),
				agentInfo("node-2", testAgentPort, caBundle),
				nodeWithAddresses("node-2", internalIP("192.0.2.20")),
			}
		})
		// One list and one watch, of the AntreaAgentInfos and of the Nodes.
		const watching = 4
		require.Equal(t, watching, f.requests())

		for i := range 20 {
			_, err := f.targetGroup.Scrape(t.Context(), "node-1")
			require.NoError(t, err)
			_, err = f.targetGroup.Scrape(t.Context(), "node-2")
			require.NoError(t, err)
			// A name which is not the one of an agent costs nothing either.
			_, err = f.targetGroup.Scrape(t.Context(), fmt.Sprintf("no-such-node-%d", i))
			requireScrapeError(t, err, ErrorCodeNotFound)
			_, err = f.targetGroup.Targets(t.Context())
			require.NoError(t, err)
			time.Sleep(time.Minute)
		}
		assert.Equal(t, watching, f.requests())
	})
}

// A Node can join after its agent was selected, and an agent which restarts gets a new self-signed
// certificate. Scrapes follow as soon as the cluster says so.
func TestAgentTargetGroupFollowsChanges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newAgentFixture(t, func(caBundle []byte) []runtime.Object {
			return []runtime.Object{agentInfo("node-1", testAgentPort, caBundle)}
		})
		_, err := f.targetGroup.Scrape(t.Context(), "node-1")
		requireScrapeError(t, err, ErrorCodeNotFound)
		_, err = f.kubeClient.CoreV1().Nodes().Create(t.Context(), nodeWithAddresses("node-1", internalIP("192.0.2.10")), metav1.CreateOptions{})
		require.NoError(t, err)
		f.sync(t)
		_, err = f.targetGroup.Scrape(t.Context(), "node-1")
		require.NoError(t, err)
		assert.Equal(t, []string{"192.0.2.10:10350"}, f.server.dialedAddrs())

		f.server.rotateCertificate(t, agentServerName)
		_, err = f.targetGroup.Scrape(t.Context(), "node-1")
		requireScrapeError(t, err, ErrorCodeTLS)
		_, err = f.dynamicClient.Resource(antreaAgentInfoGVR).Update(t.Context(), agentInfo("node-1", testAgentPort, f.server.caBundle), metav1.UpdateOptions{})
		require.NoError(t, err)
		f.sync(t)
		_, err = f.targetGroup.Scrape(t.Context(), "node-1")
		require.NoError(t, err)
	})
}

// The target group keeps one object per Node of the cluster in memory, and of each only what a
// scrape needs.
func TestAgentTargetGroupTrimsWhatItWatches(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newAgentFixture(t, func(caBundle []byte) []runtime.Object {
			info := agentInfo("node-1", testAgentPort, caBundle)
			info.Object["ovsInfo"] = map[string]any{"version": "3.5.0", "bridgeName": "br-int"}
			info.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: "antrea-agent"}})
			node := nodeWithAddresses("node-1", internalIP("192.0.2.10"))
			node.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "kubelet"}}
			node.Labels = map[string]string{"kubernetes.io/hostname": "node-1"}
			node.Status.Images = []corev1.ContainerImage{{Names: []string{"antrea/antrea-agent-ubuntu:latest"}, SizeBytes: 1 << 30}}
			return []runtime.Object{info, node}
		})

		obj, found, err := f.targetGroup.agentInfos.GetStore().GetByKey("node-1")
		require.NoError(t, err)
		require.True(t, found)
		kept := obj.(*unstructured.Unstructured).Object
		assert.ElementsMatch(t, []string{"metadata", "apiPort", "apiCABundle"}, slices.Collect(maps.Keys(kept)))
		assert.Subset(t, []string{"name", "resourceVersion"}, slices.Collect(maps.Keys(kept["metadata"].(map[string]any))))

		obj, found, err = f.targetGroup.nodes.GetStore().GetByKey("node-1")
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, nodeWithAddresses("node-1", internalIP("192.0.2.10")), obj)

		// What is left is what a scrape needs.
		_, err = f.targetGroup.Scrape(t.Context(), "node-1")
		require.NoError(t, err)
	})
}

type fakeControllerConnInfo struct {
	host       string
	serverName string
	caBundle   []byte
	err        error
}

func (p *fakeControllerConnInfo) ConnInfo() (string, string, []byte, error) {
	return p.host, p.serverName, p.caBundle, p.err
}

func TestControllerTargetGroup(t *testing.T) {
	const serverName = "antrea.kube-system.svc"
	handler := &expositionHandler{body: testExposition}
	server := newMetricsServer(t, serverName, handler)
	provider := &fakeControllerConnInfo{host: serverName, serverName: serverName, caBundle: server.caBundle}
	targetGroup := NewControllerTargetGroup(provider, NewScraper(&staticTokenSource{token: testToken}, server.dial))

	assert.Equal(t, TargetGroupController, targetGroup.Name())
	assert.False(t, targetGroup.Instanced())
	targets, err := targetGroup.Targets(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []apisv1.MetricsTarget{{ID: "antrea-controller"}}, targets)

	families, err := targetGroup.Scrape(t.Context(), "")
	require.NoError(t, err)
	assert.Len(t, families, 2)
	// No port: the Service is reached on the default HTTPS port.
	assert.Equal(t, []string{serverName + ":443"}, server.dialedAddrs())
	requests := handler.received()
	require.Len(t, requests, 1)
	assert.Equal(t, serverName, requests[0].TLS.ServerName)

	// The connection information is read for each scrape, which is what makes the rotation of
	// the Antrea CA and a restarted port forward transparent.
	server.rotateCertificate(t, serverName)
	provider.caBundle = server.caBundle
	provider.host = "localhost:41234"
	_, err = targetGroup.Scrape(t.Context(), "")
	require.NoError(t, err)
	assert.Equal(t, []string{serverName + ":443", "localhost:41234"}, server.dialedAddrs())

	provider.err = errors.New("not ready")
	_, err = targetGroup.Scrape(t.Context(), "")
	requireScrapeError(t, err, ErrorCodeUnavailable)
	assert.Len(t, server.dialedAddrs(), 2)
}

func TestSelfTargetGroup(t *testing.T) {
	registry := prometheus.NewRegistry()
	counter := prometheus.NewCounter(prometheus.CounterOpts{Name: "antrea_ui_test_total", Help: "A test counter."})
	registry.MustRegister(counter)
	counter.Add(3)
	targetGroup := NewSelfTargetGroup(registry)

	assert.Equal(t, TargetGroupSelf, targetGroup.Name())
	assert.False(t, targetGroup.Instanced())
	targets, err := targetGroup.Targets(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []apisv1.MetricsTarget{{ID: "antrea-ui"}}, targets)

	families, err := targetGroup.Scrape(t.Context(), "")
	require.NoError(t, err)
	require.Len(t, families, 1)
	assert.Equal(t, "antrea_ui_test_total", families[0].GetName())
	assert.Equal(t, 3.0, families[0].GetMetric()[0].GetCounter().GetValue())
}

func TestRegistryRejectsDuplicateName(t *testing.T) {
	registry := NewRegistry(NewSelfTargetGroup(prometheus.NewRegistry()))
	assert.Panics(t, func() { registry.Reserve(TargetGroupSelf, "taken") })
	assert.Panics(t, func() {
		NewRegistry(NewSelfTargetGroup(prometheus.NewRegistry()), NewSelfTargetGroup(prometheus.NewRegistry()))
	})
}
