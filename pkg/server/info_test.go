package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

func createTestControllerInfo(ctx context.Context, k8sClient dynamic.Interface, name string) error {
	controllerInfo := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": controllerInfoGVR.Group + "/" + controllerInfoGVR.Version,
			"kind":       "AntreaControllerInfo",
			"metadata": map[string]interface{}{
				"name": name,
			},
			// an empty Spec is fine for the sake of the test
			"spec": map[string]interface{}{},
		},
	}
	_, err := k8sClient.Resource(controllerInfoGVR).Create(ctx, controllerInfo, metav1.CreateOptions{})
	return err
}

func TestGetControllerInfo(t *testing.T) {
	ctx := context.Background()
	ts := newTestServer(t)
	require.NoError(t, createTestControllerInfo(ctx, ts.k8sClient, "antrea-controller"), "failed top create test ControllerInfo")

	req, err := http.NewRequest("GET", "/api/v1/info/controller", nil)
	require.NoError(t, err)
	ts.authorizeRequest(req)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "antrea-controller", gjson.GetBytes(rr.Body.Bytes(), "metadata.name").String())
}

func createTestAgentInfo(ctx context.Context, k8sClient dynamic.Interface, name string) error {
	agentInfo := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": agentInfoGVR.Group + "/" + agentInfoGVR.Version,
			"kind":       "AntreaAgentInfo",
			"metadata": map[string]interface{}{
				"name": name,
			},
			// an empty Spec is fine for the sake of the test
			"spec": map[string]interface{}{},
		},
	}
	_, err := k8sClient.Resource(agentInfoGVR).Create(ctx, agentInfo, metav1.CreateOptions{})
	return err
}

func TestGetAgentInfo(t *testing.T) {
	ctx := context.Background()
	ts := newTestServer(t)
	require.NoError(t, createTestAgentInfo(ctx, ts.k8sClient, "node-A"), "failed top create test AgentInfo")

	t.Run("valid name", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/api/v1/info/agents/node-A", nil)
		require.NoError(t, err)
		ts.authorizeRequest(req)
		rr := httptest.NewRecorder()
		ts.router.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "node-A", gjson.GetBytes(rr.Body.Bytes(), "metadata.name").String())
	})

	t.Run("invalid name", func(t *testing.T) {
		req, err := http.NewRequest("GET", "/api/v1/info/agents/node-B", nil)
		require.NoError(t, err)
		ts.authorizeRequest(req)
		rr := httptest.NewRecorder()
		ts.router.ServeHTTP(rr, req)
		assert.Equal(t, http.StatusNotFound, rr.Code)
	})
}

func TestGetAgentInfos(t *testing.T) {
	ctx := context.Background()
	ts := newTestServer(t)
	require.NoError(t, createTestAgentInfo(ctx, ts.k8sClient, "node-A"), "failed top create test AgentInfo")
	require.NoError(t, createTestAgentInfo(ctx, ts.k8sClient, "node-B"), "failed top create test AgentInfo")

	req, err := http.NewRequest("GET", "/api/v1/info/agents", nil)
	require.NoError(t, err)
	ts.authorizeRequest(req)
	rr := httptest.NewRecorder()
	ts.router.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Len(t, gjson.ParseBytes(rr.Body.Bytes()).Array(), 2)
}
