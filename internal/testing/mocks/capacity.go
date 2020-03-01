// Code generated by MockGen. DO NOT EDIT.
// Source: github.com/abicky/ecsmec/internal/capacity (interfaces: Drainer)

// Package mocks is a generated GoMock package.
package mocks

import (
	gomock "github.com/golang/mock/gomock"
	reflect "reflect"
)

// MockDrainer is a mock of Drainer interface
type MockDrainer struct {
	ctrl     *gomock.Controller
	recorder *MockDrainerMockRecorder
}

// MockDrainerMockRecorder is the mock recorder for MockDrainer
type MockDrainerMockRecorder struct {
	mock *MockDrainer
}

// NewMockDrainer creates a new mock instance
func NewMockDrainer(ctrl *gomock.Controller) *MockDrainer {
	mock := &MockDrainer{ctrl: ctrl}
	mock.recorder = &MockDrainerMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use
func (m *MockDrainer) EXPECT() *MockDrainerMockRecorder {
	return m.recorder
}

// Drain mocks base method
func (m *MockDrainer) Drain(arg0 []string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Drain", arg0)
	ret0, _ := ret[0].(error)
	return ret0
}

// Drain indicates an expected call of Drain
func (mr *MockDrainerMockRecorder) Drain(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Drain", reflect.TypeOf((*MockDrainer)(nil).Drain), arg0)
}
