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
	"strings"

	pluginapi "k8s.io/kubernetes/pkg/kubelet/apis/device-plugin/v1alpha1"
)

// NewDevice returns a pluginapi.Device
func NewDevice(id string) *pluginapi.Device {
	return &pluginapi.Device{
		ID: id,
	}
}

func copyDevices(devs []*pluginapi.Device) []*pluginapi.Device {
	var clones []*pluginapi.Device
	for _, d := range devs {
		clones = append(clones, d)
	}

	return clones
}

// HasDevice returns then index of the device and if the device
// is present in the list
func GetDevice(d *pluginapi.Device, devs []*pluginapi.Device) (*pluginapi.Device, bool) {
	name := DeviceKey(d)

	for _, d := range devs {
		if DeviceKey(d) != name {
			continue
		}

		return d, true
	}

	return nil, false
}

// IsVendorValid returns an error if the vendor is invalid,
// Expecting vendor to be trimed
func IsResourceNameValid(resourceName string) error {
	if resourceName == "" {
		return fmt.Errorf(pluginapi.ErrEmptyResourceName)
	}

	if strings.ContainsAny(resourceName, pluginapi.InvalidChars) {
		return fmt.Errorf(pluginapi.ErrInvalidResourceName)
	}

	return nil
}

func DeviceKey(d *pluginapi.Device) string {
	return d.ID
}
