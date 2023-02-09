package traceflow

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/clock"
	clocktesting "k8s.io/utils/clock/testing"
)

func setup(t *testing.T, clock clock.Clock) (*requestsHandler, *dynamicfake.FakeDynamicClient) {
	logger := testr.New(t)
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(traceflowGVR.GroupVersion().WithKind("TraceflowList"), &unstructured.UnstructuredList{})
	k8sClient := dynamicfake.NewSimpleDynamicClient(scheme)
	handler := newRequestsHandlerWithClock(logger, k8sClient, clock)
	return handler, k8sClient
}

func getTraceflow() map[string]interface{} {
	return map[string]interface{}{
		"spec": map[string]interface{}{
			"source": map[string]interface{}{
				"namespace": "default",
				"pod":       "podX",
			},
			"destination": map[string]interface{}{
				"namespace": "default",
				"pod":       "podY",
			},
			// we could populate other fields, but it doesn't matter for the tests
		},
	}
}

func TestRequestsHandler(t *testing.T) {
	ctx := context.Background()

	testCases := []struct {
		name  string
		phase string
	}{
		{
			name:  "success",
			phase: "Succeeded",
		},
		{
			name:  "failure",
			phase: "Failed",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			h, k8sClient := setup(t, &clock.RealClock{})
			request := &Request{
				Object: getTraceflow(),
			}

			requestID, err := h.CreateRequest(ctx, request)
			tfName := requestID
			require.NoError(t, err)

			// TF status not updated yet, hence request not ready
			status, err := h.GetRequestStatus(ctx, requestID)
			require.NoError(t, err)
			assert.False(t, status.Done)
			assert.NoError(t, status.Err)

			traceflow, err := k8sClient.Resource(traceflowGVR).Get(ctx, tfName, metav1.GetOptions{})
			require.NoError(t, err)
			traceflow.Object["status"] = map[string]interface{}{
				"phase": tc.phase,
			}
			_, err = k8sClient.Resource(traceflowGVR).Update(ctx, traceflow, metav1.UpdateOptions{})
			require.NoError(t, err)

			status, err = h.GetRequestStatus(ctx, requestID)
			require.NoError(t, err)
			assert.True(t, status.Done)
			assert.NoError(t, status.Err)

			tf, err := h.GetRequestResult(ctx, requestID)
			require.NoError(t, err)
			phase, ok, err := unstructured.NestedString(tf, "status", "phase")
			require.NoError(t, err)
			assert.True(t, ok)
			assert.Equal(t, tc.phase, phase)
			name, ok, err := unstructured.NestedString(tf, "metadata", "name")
			require.NoError(t, err)
			assert.True(t, ok)
			assert.Equal(t, tfName, name)
		})
	}
}

func TestRequestsHandlerGC(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	clock := clocktesting.NewFakeClock(now)
	h, k8sClient := setup(t, clock)
	k8sClient.PrependReactor("create", "traceflows", func(action k8stesting.Action) (bool, runtime.Object, error) {
		tf := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		tf.SetCreationTimestamp(metav1.NewTime(clock.Now()))
		return false, tf, nil
	})
	stopCh := make(chan struct{})
	defer close(stopCh)
	go h.Run(stopCh)

	request := &Request{
		Object: getTraceflow(),
	}

	requestID, err := h.CreateRequest(ctx, request)
	tfName := requestID
	require.NoError(t, err)

	_, err = k8sClient.Resource(traceflowGVR).Get(ctx, tfName, metav1.GetOptions{})
	require.NoError(t, err)

	clock.SetTime(now.Add(traceflowExpiryTimeout - 1*time.Minute))
	assert.Never(t, func() bool {
		_, err := k8sClient.Resource(traceflowGVR).Get(ctx, tfName, metav1.GetOptions{})
		return err != nil
	}, 1*time.Second, 100*time.Millisecond)

	clock.SetTime(now.Add(traceflowExpiryTimeout + 1*time.Minute))
	assert.Eventually(t, func() bool {
		_, err := k8sClient.Resource(traceflowGVR).Get(ctx, tfName, metav1.GetOptions{})
		return err != nil
	}, 1*time.Second, 100*time.Millisecond, "Traceflow should be deleted by GC")
}
