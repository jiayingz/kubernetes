/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package pluginwatcher

import (
	"fmt"
	"net"
	"sync"

	"github.com/golang/glog"
	"golang.org/x/net/context"
	"google.golang.org/grpc"

	watcherapi "k8s.io/kubernetes/pkg/kubelet/apis/pluginregistration/v1alpha"
	v1beta1 "k8s.io/kubernetes/pkg/kubelet/util/pluginwatcher/example_plugin_apis/v1beta1"
	v1beta2 "k8s.io/kubernetes/pkg/kubelet/util/pluginwatcher/example_plugin_apis/v1beta2"
)

// examplePlugin is a sample plugin to work with plugin watcher
type examplePlugin struct {
	grpcServer         *grpc.Server
	stop               chan bool
	registrationStatus chan watcherapi.RegistrationStatus  // for testing
}

type pluginServiceV1Beta1 struct {
	server *examplePlugin
}

func (s *pluginServiceV1Beta1) GetExampleInfo(ctx context.Context, rqt *v1beta1.ExampleRequest) (*v1beta1.ExampleResponse, error) {
	glog.Infof("GetExampleInfo v1beta2_field: %s", rqt.V1Beta1Field)
	resp := new(v1beta1.ExampleResponse)
	return resp, nil
}

func (s *pluginServiceV1Beta1) RegisterService() {
	v1beta1.RegisterExampleServer(s.server.grpcServer, s)
}

type pluginServiceV1Beta2 struct {
	server *examplePlugin
}

func (s *pluginServiceV1Beta2) GetExampleInfo(ctx context.Context, rqt *v1beta2.ExampleRequest) (*v1beta2.ExampleResponse, error) {
	glog.Infof("GetExampleInfo v1beta2_field: %s", rqt.V1Beta2Field)
	resp := new(v1beta2.ExampleResponse)
	return resp, nil
}

func (s *pluginServiceV1Beta2) RegisterService() {
	v1beta2.RegisterExampleServer(s.server.grpcServer, s)
}

// NewExamplePlugin returns an initialized examplePlugin instance
func NewExamplePlugin() *examplePlugin {
	return &examplePlugin{
		stop:               make(chan bool),
	}
}

// NewTestExamplePlugin returns an initialized examplePlugin instance for testing
func NewTestExamplePlugin() *examplePlugin {
        return &examplePlugin{
                stop:               make(chan bool),
		registrationStatus: make(chan watcherapi.RegistrationStatus),
        }
}

// GetInfo is the RPC invoked by plugin watcher
func (e *examplePlugin) GetInfo(stream watcherapi.Registration_GetInfoServer) error {
	info := watcherapi.PluginInfo{
		Type:    "example-plugin-type",
		Name:    "example-plugin",
		Version: []string{"v1beta1", "v1beta2"},
	}
	err := stream.Send(&info)
	if err != nil {
		glog.Errorf("Error sending plugin info: %v\n", err)
		return err
	}

	s, err := stream.Recv()
	if err != nil {
		glog.Errorf("Received err: %v\n", err)
		return err
	}
	glog.Infof("Got stream: %v\n", s)
	if e.registrationStatus != nil {
		e.registrationStatus <- *s
	}
	if s.PluginRegistered {
		glog.Infof("Registration success!!\n")
		return nil
	} else {
		glog.Errorf("Registration failed: %s\n", s.Error)
		return fmt.Errorf("Registration failed: %s", s.Error)
	}
}

// Serve starts example plugin grpc server
func (e *examplePlugin) Serve(socketPath string) error {
	glog.Infof("starting example server at: %s\n", socketPath)
	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		glog.Errorf("starting example server failed: %v", err)
		return err
	}
	glog.Infof("example server started at: %s\n", socketPath)
	e.grpcServer = grpc.NewServer()
	// Registers kubelet plugin watcher api.
	watcherapi.RegisterRegistrationServer(e.grpcServer, e)
	// Registers services for both v1beta1 and v1beta2 versions.
	v1beta1 := &pluginServiceV1Beta1{server: e}
	v1beta1.RegisterService()
	v1beta2 := &pluginServiceV1Beta2{server: e}
	v1beta2.RegisterService()

	// Starts service
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Blocking call to accept incoming connections.
		err := e.grpcServer.Serve(lis)
		glog.Errorf("example server stopped serving: %v", err)
	}()

	<-e.stop
	e.grpcServer.Stop()
	wg.Wait()
	close(e.stop)
	return nil
}

func (e *examplePlugin) Stop() {
	glog.Infof("stopping example server\n")
	e.stop <- true
	<-e.stop
}
