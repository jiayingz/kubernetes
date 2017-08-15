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

package cm

import (
	"fmt"
	"strings"
	"sync"

	"github.com/golang/glog"

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubernetes/pkg/kubelet/apis/device-plugin/v1alpha1"
	"k8s.io/kubernetes/pkg/kubelet/device-plugin"
)

// podDevices represents a list of pod to Device mappings.
type containerToDevice map[string]sets.String
type podDevices struct {
	podDeviceMapping map[string]containerToDevice
}

func newPodDevices() *podDevices {
	return &podDevices{
		podDeviceMapping: make(map[string]containerToDevice),
	}
}
func (pdev *podDevices) pods() sets.String {
	ret := sets.NewString()
	for k := range pdev.podDeviceMapping {
		ret.Insert(k)
	}
	return ret
}

func (pdev *podDevices) insert(podUID, contName string, device string) {
	if _, exists := pdev.podDeviceMapping[podUID]; !exists {
		pdev.podDeviceMapping[podUID] = make(containerToDevice)
	}
	if _, exists := pdev.podDeviceMapping[podUID][contName]; !exists {
		pdev.podDeviceMapping[podUID][contName] = sets.NewString()
	}
	pdev.podDeviceMapping[podUID][contName].Insert(device)
}

func (pdev *podDevices) getDevices(podUID, contName string) sets.String {
	containers, exists := pdev.podDeviceMapping[podUID]
	if !exists {
		return nil
	}
	devices, exists := containers[contName]
	if !exists {
		return nil
	}
	return devices
}

func (pdev *podDevices) delete(pods []string) {
	for _, uid := range pods {
		delete(pdev.podDeviceMapping, uid)
	}
}

func (pdev *podDevices) devices() sets.String {
	ret := sets.NewString()
	for _, containerToDevice := range pdev.podDeviceMapping {
		for _, deviceSet := range containerToDevice {
			ret = ret.Union(deviceSet)
		}
	}
	return ret
}

type DevicePluginHandlerImpl struct {
	sync.Mutex
	devicePluginManager *deviceplugin.ManagerImpl
	// allDevices and allocated are keyed by resource_name.
	allDevices map[string]sets.String
	allocated  map[string]*podDevices
}

// NewDevicePluginHandler create a DevicePluginHandler
func NewDevicePluginHandlerImpl() (*DevicePluginHandlerImpl, error) {
	glog.V(2).Infof("Starting Device Plugin Handler")

	mgr, err := deviceplugin.NewManagerImpl(pluginapi.KubeletSocket,
		func([]*pluginapi.Device) {})

	if err != nil {
		return nil, fmt.Errorf("Failed to initialize device plugin: %+v", err)
	}

	if err := mgr.Start(); err != nil {
		return nil, err
	}

	return &DevicePluginHandlerImpl{
		devicePluginManager: mgr,
		allDevices:          make(map[string]sets.String),
		allocated:           devicesInUse(),
	}, nil
}

// devicesInUse returns a list of custom devices in use along with the
// respective pods that are using them.
func devicesInUse() map[string]*podDevices {
	// TODO: gets the initial state from checkpointing.
	return make(map[string]*podDevices)
}

// updateAllDevices gets all healthy devices.
// TODO: consider how to make this more efficient.
func (h *DevicePluginHandlerImpl) updateAllDevices() {
	h.allDevices = make(map[string]sets.String)
	for k, devs := range h.devicePluginManager.Devices() {
		key := v1.ResourceOpaqueIntPrefix + k
		h.allDevices[key] = sets.NewString()
		for _, dev := range devs {
			glog.V(2).Infof("insert device %s for resource %s", dev.ID, key)
			h.allDevices[key].Insert(dev.ID)
		}
	}
}

// updateAllocatedGPUs updates the list of GPUs in use.
// It gets a list of active pods and then frees any GPUs that are bound to
// terminated pods. Returns error on failure.
func (h *DevicePluginHandlerImpl) updateAllocatedDevices(activePods []*v1.Pod) {
	activePodUids := sets.NewString()
	for _, pod := range activePods {
		activePodUids.Insert(string(pod.UID))
	}
	for _, v := range h.allocated {
		allocatedPodUids := v.pods()
		podsToBeRemoved := allocatedPodUids.Difference(activePodUids)
		glog.V(5).Infof("pods to be removed: %v", podsToBeRemoved.List())
		v.delete(podsToBeRemoved.List())
	}
}

func (h *DevicePluginHandlerImpl) Devices() map[string][]*pluginapi.Device {
	return h.devicePluginManager.Devices()
}

func (h *DevicePluginHandlerImpl) Allocate(pod *v1.Pod, container *v1.Container, activePods []*v1.Pod) ([]*pluginapi.AllocateResponse, error) {
	glog.V(2).Infof("Allocate called")
	h.Lock()
	defer h.Unlock()
	h.updateAllDevices()
	h.updateAllocatedDevices(activePods)
	var ret []*pluginapi.AllocateResponse
	for k, v := range container.Resources.Limits {
		key := string(k)
		needed := int(v.Value())
		glog.V(2).Infof("needs %d %s", needed, key)
		if !deviceplugin.IsDeviceName(k) || needed == 0 {
			continue
		}
		// Gets list of devices that have already been allocated.
		// This can happen if a container restarts for example.
		if h.allocated[key] == nil {
			h.allocated[key] = newPodDevices()
		}
		devices := h.allocated[key].getDevices(string(pod.UID), container.Name)
		if devices != nil {
			glog.V(2).Infof("Found pre-allocated devices for container %q in Pod %q: %v", container.Name, pod.UID, devices.List())
			needed = needed - devices.Len()
		}
		// Get Devices in use.
		devicesInUse := h.allocated[key].devices()
		glog.V(2).Infof("all devices: %v", h.allDevices[key].List())
		glog.V(2).Infof("devices in use: %v", devicesInUse.List())
		// Get a list of available devices.
		available := h.allDevices[key].Difference(devicesInUse)
		glog.V(2).Infof("devices available: %v", available.List())
		if int(available.Len()) < needed {
			return nil, fmt.Errorf("requested number of devices unavailable. Requested: %d, Available: %d", v, available.Len())
		}
		resp, err := h.devicePluginManager.Allocate(strings.TrimPrefix(key, v1.ResourceOpaqueIntPrefix),
			append(devices.UnsortedList(), available.UnsortedList()[:needed]...))
		if err != nil {
			return nil, err
		}
		ret = append(ret, resp)
	}
	return ret, nil
}
