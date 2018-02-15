/*
Copyright 2017 The Kubernetes Authors.

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

package devicemanager

import (
	"io"
	"log"
	"net"
	"os"
	"path"
	"time"

	"golang.org/x/net/context"
	"google.golang.org/grpc"

	pluginapi "k8s.io/kubernetes/pkg/kubelet/apis/deviceplugin/v1alpha"
	watcherapi "k8s.io/kubernetes/pkg/kubelet/apis/pluginregistration/v1alpha"
	//"k8s.io/kubernetes/pkg/kubelet/util/pluginwatcher"
)

// Stub implementation for DevicePlugin.
type Stub struct {
	devs         []*pluginapi.Device
	socket       string
	resourceName string

	stop   chan interface{}
	update chan []*pluginapi.Device

	server *grpc.Server

	// allocFunc is used for handling allocation request
	allocFunc stubAllocFunc
}

// stubAllocFunc is the function called when receive an allocation request from Kubelet
type stubAllocFunc func(r *pluginapi.AllocateRequest, devs map[string]pluginapi.Device) (*pluginapi.AllocateResponse, error)

func defaultAllocFunc(r *pluginapi.AllocateRequest, devs map[string]pluginapi.Device) (*pluginapi.AllocateResponse, error) {
	var response pluginapi.AllocateResponse

	return &response, nil
}

// NewDevicePluginStub returns an initialized DevicePlugin Stub.
func NewDevicePluginStub(devs []*pluginapi.Device, socket string, name string) *Stub {
	return &Stub{
		devs:         devs,
		socket:       socket,
		resourceName: name,

		stop:   make(chan interface{}),
		update: make(chan []*pluginapi.Device),

		allocFunc: defaultAllocFunc,
	}
}

// SetAllocFunc sets allocFunc of the device plugin
func (m *Stub) SetAllocFunc(f stubAllocFunc) {
	m.allocFunc = f
}

// Start starts the gRPC server of the device plugin
func (m *Stub) Start() error {
	err := m.cleanup()
	if err != nil {
		return err
	}

	sock, err := net.Listen("unix", m.socket)
	if err != nil {
		return err
	}

	m.server = grpc.NewServer([]grpc.ServerOption{}...)
	pluginapi.RegisterDevicePluginServer(m.server, m)
	watcherapi.RegisterRegistrationServer(m.server, m)

	go m.server.Serve(sock)
	_, conn, err := dial(m.socket)
	if err != nil {
		return err
	}
	conn.Close()
	log.Println("Starting to serve on", m.socket)

	return nil
}

// Stop stops the gRPC server
func (m *Stub) Stop() error {
	m.server.Stop()
	close(m.stop)

	return m.cleanup()
}

// GetInfo is the RPC which return pluginInfo
func (m *Stub) GetInfo(stream watcherapi.Registration_GetInfoServer) error {
	log.Println("GetInfo")
	info := watcherapi.PluginInfo{
		Type:    watcherapi.DevicePlugin,
		Name:    m.resourceName,
		Version: []string{pluginapi.Version},
	}
	err := stream.Send(&info)
	if err != nil {
		log.Println("error sending plugin info: ", err)
	}

	for {
		s, err := stream.Recv()
		log.Println("Got ", s)
		if err == io.EOF {
			log.Println("Breaking err ", err)
			break
		}
		if err != nil {
			log.Println("Received err ", err)
			return err
		}
		if s.PluginRegistered {
			log.Println("Registration success!!")
			break
		}
		if s.Error != "" {
			log.Println(s.Error)
			break
		}
	}

	return nil
}

// Register registers the device plugin for the given resourceName with Kubelet.
func (m *Stub) Register(kubeletEndpoint, resourceName string) error {
	conn, err := grpc.Dial(kubeletEndpoint, grpc.WithInsecure(), grpc.WithBlock(),
		grpc.WithTimeout(10*time.Second),
		grpc.WithDialer(func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("unix", addr, timeout)
		}))
	defer conn.Close()
	if err != nil {
		return err
	}
	client := pluginapi.NewRegistrationClient(conn)
	reqt := &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     path.Base(m.socket),
		ResourceName: resourceName,
	}

	_, err = client.Register(context.Background(), reqt)
	if err != nil {
		return err
	}
	return nil
}

// ListAndWatch lists devices and update that list according to the Update call
func (m *Stub) ListAndWatch(e *pluginapi.Empty, s pluginapi.DevicePlugin_ListAndWatchServer) error {
	log.Println("ListAndWatch")

	s.Send(&pluginapi.ListAndWatchResponse{Devices: m.devs})

	for {
		select {
		case <-m.stop:
			return nil
		case updated := <-m.update:
			s.Send(&pluginapi.ListAndWatchResponse{Devices: updated})
		}
	}
}

// Update allows the device plugin to send new devices through ListAndWatch
func (m *Stub) Update(devs []*pluginapi.Device) {
	m.update <- devs
}

// Allocate does a mock allocation
func (m *Stub) Allocate(ctx context.Context, r *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	log.Printf("Allocate, %+v", r)

	devs := make(map[string]pluginapi.Device)

	for _, dev := range m.devs {
		devs[dev.ID] = *dev
	}

	return m.allocFunc(r, devs)
}

func (m *Stub) cleanup() error {
	if err := os.Remove(m.socket); err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}
