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
	"uuid"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
)

const (
	traceflowExpiryTimeout = 60 * time.Minute
	gcPeriod               = 1 * time.Minute
)

var (
	traceflowGVR = schema.GroupVersionResource{
		Group:    "crd.antrea.io",
		Version:  "v1beta1",
		Resource: "traceflows",
	}

	traceflowLabels = map[string]string{
		"ui.antrea.io": "",
	}
)

type requestsHandler struct {
	logger logr.Logger
	// gcClient is only used by the background GC loop, which runs with no user request in
	// flight and so has to act as antrea-ui-admin. User-initiated operations take their client
	// as an argument instead.
	gcClient dynamic.Interface
}

func NewRequestsHandler(logger logr.Logger, gcClient dynamic.Interface) *requestsHandler {
	return &requestsHandler{
		logger:   logger,
		gcClient: gcClient,
	}
}

func (h *requestsHandler) Run(stopCh <-chan struct{}) {
	go h.runGC(stopCh)
	<-stopCh
}

func (h *requestsHandler) CreateRequest(ctx context.Context, client dynamic.Interface, request *Request) (string, error) {
	requestID := uuid.New().String()
	if err := h.createTraceflow(ctx, client, requestID, request.Object); err != nil {
		return "", err
	}
	return requestID, nil
}

func (h *requestsHandler) GetRequestResult(ctx context.Context, client dynamic.Interface, requestID string) (map[string]any, bool, error) {
	return h.getTraceflow(ctx, client, requestID)
}

func (h *requestsHandler) DeleteRequest(ctx context.Context, client dynamic.Interface, requestID string) (bool, error) {
	tfName := requestID
	err := client.Resource(traceflowGVR).Delete(ctx, tfName, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
func (h *requestsHandler) getTraceflow(ctx context.Context, client dynamic.Interface, tfName string) (map[string]any, bool, error) {
	traceflow, err := client.Resource(traceflowGVR).Get(ctx, tfName, metav1.GetOptions{})
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

func (h *requestsHandler) createTraceflow(ctx context.Context, client dynamic.Interface, tfName string, object map[string]any) error {
	traceflow := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": traceflowGVR.Group + "/" + traceflowGVR.Version,
			"kind":       "Traceflow",
			"metadata": map[string]any{
				"name": tfName,
			},
			"spec": object["spec"],
		},
	}
	traceflow.SetLabels(traceflowLabels)
	if _, err := client.Resource(traceflowGVR).Create(ctx, traceflow, metav1.CreateOptions{}); err != nil {
		return err
	}
	return nil
}

func (h *requestsHandler) doGC(ctx context.Context) {
	list, err := h.gcClient.Resource(traceflowGVR).List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(traceflowLabels).String(),
	})
	if err != nil {
		h.logger.Error(err, "Error when listing traceflows")
		return
	}
	expiredTraceflows := []string{}
	now := time.Now()
	for idx := range list.Items {
		tf := &list.Items[idx]
		creationTimestamp := tf.GetCreationTimestamp()
		if now.Sub(creationTimestamp.Time) > traceflowExpiryTimeout {
			expiredTraceflows = append(expiredTraceflows, tf.GetName())
		}
	}
	for _, tfName := range expiredTraceflows {
		if err := h.gcClient.Resource(traceflowGVR).Delete(ctx, tfName, metav1.DeleteOptions{}); err != nil {
			h.logger.Error(err, "Error when deleting expired traceflow", "name", tfName)
		}
	}
}

func (h *requestsHandler) runGC(stopCh <-chan struct{}) {
	wait.UntilWithContext(wait.ContextForChannel(stopCh), h.doGC, gcPeriod)
}
