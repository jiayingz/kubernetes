/*
Copyright 2016 The Kubernetes Authors.

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

package deviceplugin

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/golang/glog"
	"golang.org/x/net/context"
	"google.golang.org/grpc"

	pluginapi "k8s.io/kubernetes/pkg/kubelet/apis/device-plugin/v1alpha1"
)

type endpoint struct {
	client pluginapi.DevicePluginClient

	socketPath   string
	resourceName string

	devices []*pluginapi.Device
	mutex   sync.Mutex

	callback MonitorCallback

	cancel context.CancelFunc
	ctx    context.Context
}

func newEndpoint(socketPath, resourceName string,
	callback MonitorCallback) (*endpoint, error) {

	client, err := dial(socketPath)
	if err != nil {
		glog.Errorf("Can't new endpoint with path %s err %v", socketPath, err)
		return nil, err
	}

	ctx, stop := context.WithCancel(context.Background())

	return &endpoint{
		client: client,

		socketPath:   socketPath,
		resourceName: resourceName,

		devices:  nil,
		callback: callback,

		cancel: stop,
		ctx:    ctx,
	}, nil
}

func (e *endpoint) List() (pluginapi.DevicePlugin_ListAndWatchClient, error) {

	stream, err := e.client.ListAndWatch(e.ctx, &pluginapi.Empty{})
	if err != nil {
		glog.Errorf("Could not call ListWatch for device plugin %s: %v",
			e.resourceName, err)

		return nil, err
	}

	devs, err := stream.Recv()
	if err != nil {
		glog.Errorf(ErrListWatch, e.resourceName, err)
		return nil, err
	}

	glog.V(2).Infof("Got devices: %+v", devs)

	e.mutex.Lock()
	e.devices = devs.Devices
	e.mutex.Unlock()

	return stream, nil
}

func (e *endpoint) ListWatch(stream pluginapi.DevicePlugin_ListAndWatchClient) {
	glog.V(2).Infof("Starting ListWatch")

	for {
		response, err := stream.Recv()
		if err != nil {
			glog.Errorf(ErrListWatch, e.resourceName, err)
			return
		}

		devs := response.Devices
		glog.V(2).Infof("State pushed for device plugin %s", e.resourceName, err)

		// TODO check this is the same list
		if len(devs) > len(e.devices) {
			glog.Errorf("New device for Device Plugin %s", e.resourceName)
			continue
		}

		var updated []*pluginapi.Device

		e.mutex.Lock()
		for i := 0; i < len(e.devices); i++ {
			d1 := e.devices[i]
			d2, ok := GetDevice(d1, devs)

			// TODO Should we remove it ?
			if !ok {
				glog.Errorf("Device lost for Device Plugin %s", e.resourceName)
				continue
			}

			if d2.State == d1.State {
				continue
			}

			glog.V(5).Infof("State changed from %s to %s for device %s",
				d1.State, d2.State, e.resourceName)

			e.devices[i].State = d2.State
			updated = append(updated, d2)
		}

		e.mutex.Unlock()

		e.callback(updated)
	}

}

func (e *endpoint) allocate(devs []string) (*pluginapi.AllocateResponse, error) {
	return e.client.Allocate(context.Background(), &pluginapi.AllocateRequest{
		DevicesIDs: devs,
	})
}

func (e *endpoint) stop() {
	e.cancel()
}

func dial(unixSocketPath string) (pluginapi.DevicePluginClient, error) {

	// TODO keepalive
	c, err := grpc.Dial(unixSocketPath, grpc.WithInsecure(),
		grpc.WithDialer(func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("unix", addr, timeout)
		}))

	if err != nil {
		return nil, fmt.Errorf(pluginapi.ErrFailedToDialDevicePlugin+" %v", err)
	}

	return pluginapi.NewDevicePluginClient(c), nil
}
