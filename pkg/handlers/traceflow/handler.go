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

package traceflow

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/utils/clock"
)

const (
	traceflowExpiryTimeout = 60 * time.Minute
	gcPeriod               = 1 * time.Minute
)

var (
	traceflowGVR = schema.GroupVersionResource{
		Group:    "crd.antrea.io",
		Version:  "v1alpha1",
		Resource: "traceflows",
	}

	traceflowLabels = map[string]string{
		"ui.antrea.io": "",
	}
)

type requestsHandler struct {
	logger    logr.Logger
	k8sClient dynamic.Interface
	clock     clock.Clock
}

func newRequestsHandlerWithClock(logger logr.Logger, k8sClient dynamic.Interface, clock clock.Clock) *requestsHandler {
	return &requestsHandler{
		logger:    logger,
		k8sClient: k8sClient,
		clock:     clock,
	}
}

func NewRequestsHandler(logger logr.Logger, k8sClient dynamic.Interface) *requestsHandler {
	return newRequestsHandlerWithClock(logger, k8sClient, &clock.RealClock{})
}

func (h *requestsHandler) Run(stopCh <-chan struct{}) {
	go h.runGC(stopCh)
	<-stopCh
}

func (h *requestsHandler) CreateRequest(ctx context.Context, request *Request) (string, error) {
	requestID := uuid.NewString()
	if err := h.createTraceflow(ctx, requestID, request.Object); err != nil {
		return "", err
	}
	return requestID, nil
}

func (h *requestsHandler) GetRequestResult(ctx context.Context, requestID string) (map[string]interface{}, bool, error) {
	return h.getTraceflow(ctx, requestID)
}

func (h *requestsHandler) DeleteRequest(ctx context.Context, requestID string) (bool, error) {
	tfName := requestID
	err := h.k8sClient.Resource(traceflowGVR).Delete(ctx, tfName, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
func (h *requestsHandler) getTraceflow(ctx context.Context, tfName string) (map[string]interface{}, bool, error) {
	traceflow, err := h.k8sClient.Resource(traceflowGVR).Get(ctx, tfName, metav1.GetOptions{})
	if err != nil {
		return nil, false, err
	}
	phase, ok, err := unstructured.NestedString(traceflow.Object, "status", "phase")
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return traceflow.Object, false, nil
	}
	return traceflow.Object, (phase == "Succeeded" || phase == "Failed"), nil
}

func (h *requestsHandler) createTraceflow(ctx context.Context, tfName string, object map[string]interface{}) error {
	traceflow := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": traceflowGVR.Group + "/" + traceflowGVR.Version,
			"kind":       "Traceflow",
			"metadata": map[string]interface{}{
				"name": tfName,
			},
			"spec": object["spec"],
		},
	}
	traceflow.SetLabels(traceflowLabels)
	if _, err := h.k8sClient.Resource(traceflowGVR).Create(ctx, traceflow, metav1.CreateOptions{}); err != nil {
		return err
	}
	return nil
}

func (h *requestsHandler) doGC(ctx context.Context) {
	list, err := h.k8sClient.Resource(traceflowGVR).List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(traceflowLabels).String(),
	})
	if err != nil {
		h.logger.Error(err, "Error when listing traceflows")
		return
	}
	expiredTraceflows := []string{}
	now := h.clock.Now()
	for _, tf := range list.Items {
		creationTimestamp := tf.GetCreationTimestamp()
		if now.Sub(creationTimestamp.Time) > traceflowExpiryTimeout {
			expiredTraceflows = append(expiredTraceflows, tf.GetName())
		}
	}
	for _, tfName := range expiredTraceflows {
		if err := h.k8sClient.Resource(traceflowGVR).Delete(ctx, tfName, metav1.DeleteOptions{}); err != nil {
			h.logger.Error(err, "Error when deleting expired traceflow", "name", tfName)
		}
	}
}

func (h *requestsHandler) runGC(stopCh <-chan struct{}) {
	ctx, cancel := wait.ContextForChannel(stopCh)
	defer cancel()
	go wait.BackoffUntil(func() {
		h.doGC(ctx)
	}, wait.NewJitteredBackoffManager(gcPeriod, 0.0, h.clock), true, stopCh)
	<-stopCh
}
