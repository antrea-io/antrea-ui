// Copyright 2024 Antrea Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

// Code generated by MockGen. DO NOT EDIT.
// Source: interface.go

// Package testing is a generated GoMock package.
package testing

import (
	context "context"
	io "io"
	reflect "reflect"

	gomock "github.com/golang/mock/gomock"
)

// MockRequestsHandler is a mock of RequestsHandler interface.
type MockRequestsHandler struct {
	ctrl     *gomock.Controller
	recorder *MockRequestsHandlerMockRecorder
}

// MockRequestsHandlerMockRecorder is the mock recorder for MockRequestsHandler.
type MockRequestsHandlerMockRecorder struct {
	mock *MockRequestsHandler
}

// NewMockRequestsHandler creates a new mock instance.
func NewMockRequestsHandler(ctrl *gomock.Controller) *MockRequestsHandler {
	mock := &MockRequestsHandler{ctrl: ctrl}
	mock.recorder = &MockRequestsHandlerMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockRequestsHandler) EXPECT() *MockRequestsHandlerMockRecorder {
	return m.recorder
}

// Request mocks base method.
func (m *MockRequestsHandler) Request(ctx context.Context, method, path string, body io.Reader) ([]byte, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Request", ctx, method, path, body)
	ret0, _ := ret[0].([]byte)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Request indicates an expected call of Request.
func (mr *MockRequestsHandlerMockRecorder) Request(ctx, method, path, body interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Request", reflect.TypeOf((*MockRequestsHandler)(nil).Request), ctx, method, path, body)
}
