// Copyright 2015-2019 Amazon.com, Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//     http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.
//

// Code generated by MockGen. DO NOT EDIT.
// source: github.com/aws/amazon-ecs-agent/agent/eni/netlinkwrapper (interfaces: NetLink)

// Package mock_netlinkwrapper is a generated GoMock package.
package mock_netlinkwrapper

import (
	reflect "reflect"

	gomock "github.com/golang/mock/gomock"
	netlink "github.com/vishvananda/netlink"
)

// MockNetLink is a mock of NetLink interface
type MockNetLink struct {
	ctrl     *gomock.Controller
	recorder *MockNetLinkMockRecorder
}

// MockNetLinkMockRecorder is the mock recorder for MockNetLink
type MockNetLinkMockRecorder struct {
	mock *MockNetLink
}

// NewMockNetLink creates a new mock instance
func NewMockNetLink(ctrl *gomock.Controller) *MockNetLink {
	mock := &MockNetLink{ctrl: ctrl}
	mock.recorder = &MockNetLinkMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use
func (m *MockNetLink) EXPECT() *MockNetLinkMockRecorder {
	return m.recorder
}

// LinkByName mocks base method
func (m *MockNetLink) LinkByName(arg0 string) (netlink.Link, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LinkByName", arg0)
	ret0, _ := ret[0].(netlink.Link)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// LinkByName indicates an expected call of LinkByName
func (mr *MockNetLinkMockRecorder) LinkByName(arg0 interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LinkByName", reflect.TypeOf((*MockNetLink)(nil).LinkByName), arg0)
}

// LinkList mocks base method
func (m *MockNetLink) LinkList() ([]netlink.Link, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LinkList")
	ret0, _ := ret[0].([]netlink.Link)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// LinkList indicates an expected call of LinkList
func (mr *MockNetLinkMockRecorder) LinkList() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LinkList", reflect.TypeOf((*MockNetLink)(nil).LinkList))
}
