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

package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sync"
	"time"

	"github.com/golang/glog"
	"golang.org/x/net/context"
	"google.golang.org/grpc"

	pluginapi "k8s.io/kubernetes/pkg/kubelet/apis/device-plugin/v1alpha1"
)

const (
	// All NVIDIA GPUs cards should be mounted with nvidiactl and nvidia-uvm
	// If the driver installed correctly, the 2 devices will be there.
	nvidiaCtlDevice string = "/dev/nvidiactl"
	nvidiaUVMDevice string = "/dev/nvidia-uvm"
	// Optional device.
	nvidiaUVMToolsDevice string = "/dev/nvidia-uvm-tools"
	devDirectory                = "/dev"
	nvidiaDeviceRE              = `^nvidia[0-9]*$`

	// Device plugin settings.
	devicePluginMountPath = "/device-plugin"
	kubeletEndpoint       = "kubelet.sock"
	resourceName          = "nvidiaGPU"
)

// nvidiaGPUManager manages nvidia gpu devices.
type nvidiaGPUManager struct {
	defaultDevices []string
	devices        map[string]pluginapi.Device
}

func NewNvidiaGPUManager() (*nvidiaGPUManager, error) {
	return &nvidiaGPUManager{
		devices: make(map[string]pluginapi.Device),
	}, nil
}

// Discovers all NVIDIA GPU devices available on the local node by walking `/dev` directory.
func (ngm *nvidiaGPUManager) discoverGPUs() error {
	reg := regexp.MustCompile(nvidiaDeviceRE)
	files, err := ioutil.ReadDir(devDirectory)
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		if reg.MatchString(f.Name()) {
			fmt.Printf("Found Nvidia GPU %q\n", f.Name())
			ngm.devices[f.Name()] = pluginapi.Device{f.Name(), pluginapi.Healthy}
		}
	}

	return nil
}

func (ngm *nvidiaGPUManager) GetDeviceState(DeviceName string) string {
	// TODO: calling Nvidia tools to figure out actual device state
	return pluginapi.Healthy
}

// Discovers Nvidia GPU devices, installs device drivers, and sets up device
// access environment.
func (ngm *nvidiaGPUManager) Start() error {
	// Install Nvidia device drivers.
	// TODO: turn this into a go library to get better error reporting.
	fmt.Printf("Run installer script\n")
	cmd := exec.Command("/bin/sh", "-c", "/usr/bin/nvidia-installer.sh")
	stdoutStderr, err := cmd.CombinedOutput()
	fmt.Printf("%s\n", stdoutStderr)

	if _, err := os.Stat(nvidiaCtlDevice); err != nil {
		return err
	}

	if _, err := os.Stat(nvidiaUVMDevice); err != nil {
		return err
	}
	ngm.defaultDevices = []string{nvidiaCtlDevice, nvidiaUVMDevice}
	_, err = os.Stat(nvidiaUVMToolsDevice)
	if !os.IsNotExist(err) {
		ngm.defaultDevices = append(ngm.defaultDevices, nvidiaUVMToolsDevice)
	}

	if err := ngm.discoverGPUs(); err != nil {
		return err
	}

	return nil
}

func Register(kubeletEndpoint string, pluginEndpoint, resourceName string) error {
	conn, err := grpc.Dial(kubeletEndpoint, grpc.WithInsecure(),
		grpc.WithDialer(func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("unix", addr, timeout)
		}))
	defer conn.Close()
	if err != nil {
		return fmt.Errorf("device-plugin: cannot connect to kubelet service: %v", err)
	}
	client := pluginapi.NewRegistrationClient(conn)
	reqt := &pluginapi.RegisterRequest{
		Version:      pluginapi.Version,
		Endpoint:     pluginEndpoint,
		ResourceName: resourceName,
	}

	_, err = client.Register(context.Background(), reqt)
	if err != nil {
		return fmt.Errorf("device-plugin: cannot register to kubelet service: %v", err)
	}
	return nil
}

// Implements DevicePlugin service functions
func (ngm *nvidiaGPUManager) ListAndWatch(emtpy *pluginapi.Empty, stream pluginapi.DevicePlugin_ListAndWatchServer) error {
	fmt.Printf("device-plugin: ListAndWatch start\n")
	changed := true
	for {
		for id, dev := range ngm.devices {
			state := ngm.GetDeviceState(id)
			if dev.State != state {
				changed = true
				dev.State = state
				ngm.devices[id] = dev
			}
		}
		if changed {
			resp := new(pluginapi.ListAndWatchResponse)
			for _, dev := range ngm.devices {
				resp.Devices = append(resp.Devices, &pluginapi.Device{dev.ID, dev.State})
			}
			fmt.Printf("ListAndWatch: send devices %v", resp)
			if err := stream.Send(resp); err != nil {
				fmt.Printf("device-plugin: cannot update device states: %v\n", err)
			}
		}
		changed = false
		time.Sleep(5 * time.Second)
	}
	return nil
}

func (ngm *nvidiaGPUManager) Allocate(ctx context.Context, rqt *pluginapi.AllocateRequest) (*pluginapi.AllocateResponse, error) {
	resp := new(pluginapi.AllocateResponse)
	for _, id := range rqt.DevicesIDs {
		dev, ok := ngm.devices[id]
		if !ok {
			return nil, fmt.Errorf("Invalid allocation request with non-existing device %s", id)
		}
		if dev.State != pluginapi.Healthy {
			return nil, fmt.Errorf("Invalid allocation request with unhealthy device %s", id)
		}
		devRuntime := new(pluginapi.DeviceRuntimeSpec)
		devRuntime.Devices = append(devRuntime.Devices, &pluginapi.DeviceSpec{
			HostPath:      "/dev/" + id,
			ContainerPath: "/dev/" + id,
			Permissions:   "mrw",
		})
		for _, d := range ngm.defaultDevices {
			devRuntime.Devices = append(devRuntime.Devices, &pluginapi.DeviceSpec{
				HostPath:      d,
				ContainerPath: d,
				Permissions:   "mrw",
			})
		}
		devRuntime.Mounts = append(devRuntime.Mounts, &pluginapi.Mount{
			HostPath:  "/home/kubernetes/bin/nvidia/lib",
			MountPath: "/usr/local/nvidia/lib64",
			ReadOnly:  true,
		})
		devRuntime.Mounts = append(devRuntime.Mounts, &pluginapi.Mount{
			HostPath:  "/home/kubernetes/bin/nvidia/bin",
			MountPath: "/usr/local/nvidia/bin",
			ReadOnly:  true,
		})
		resp.Spec = append(resp.Spec, devRuntime)
	}
	return resp, nil
}

func main() {
	flag.Parse()
	fmt.Printf("device-plugin started\n")
	ngm, err := NewNvidiaGPUManager()
	if err != nil {
		glog.Fatal(err)
		os.Exit(1)
	}
	err = ngm.Start()
	if err != nil {
		glog.Fatal(err)
		os.Exit(1)
	}

	pluginEndpoint := fmt.Sprintf("%s-%d.sock", resourceName, time.Now().Unix())
	serverStarted := make(chan bool)
	var wg sync.WaitGroup
	wg.Add(1)
	// Starts device plugin service.
	go func() {
		defer wg.Done()
		fmt.Printf("device-plugin start server at: %s\n", path.Join(devicePluginMountPath, pluginEndpoint))
		lis, err := net.Listen("unix", path.Join(devicePluginMountPath, pluginEndpoint))
		if err != nil {
			glog.Fatal(err)
			return
		}
		grpcServer := grpc.NewServer()
		pluginapi.RegisterDevicePluginServer(grpcServer, ngm)
		serverStarted <- true
		grpcServer.Serve(lis)
	}()

	<-serverStarted
	time.Sleep(5 * time.Second)

	// Registers with Kubelet.
	err = Register(path.Join(devicePluginMountPath, kubeletEndpoint), pluginEndpoint, resourceName)
	if err != nil {
		glog.Fatal(err)
		os.Exit(1)
	}
	fmt.Printf("device-plugin registered\n")
	wg.Wait()
}
