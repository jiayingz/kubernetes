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
	"io/ioutil"
	"net"
	"testing"
	"time"

	"github.com/golang/glog"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/context"
	"google.golang.org/grpc"

	v1beta1 "k8s.io/kubernetes/pkg/kubelet/util/pluginwatcher/example_plugin_apis/v1beta1"
	v1beta2 "k8s.io/kubernetes/pkg/kubelet/util/pluginwatcher/example_plugin_apis/v1beta2"
)

func TestExamplePlugin(t *testing.T) {
	socketDir, err := ioutil.TempDir("", "device_plugin")
	require.NoError(t, err)
	socketPath := socketDir + "/plugin.sock"
        w := NewWatcher(socketDir)

	testCases := []struct {
                description     string
		returnErr	error
	}{
                {
                        description: "Successfully register plugin through inotify",
                        returnErr:   nil,
                },
                {
                        description: "Successfully register plugin through inotify after plugin restarts",
                        returnErr:   nil,
                },
                {
                        description: "Fails registration with conflicting plugin name",
                        returnErr:   fmt.Errorf("conflicting plugin name"),
                },
                {
                        description: "Successfully register plugin during initial traverse after plugin watcher restarts",
                        returnErr:   nil,
                },
                {
                        description: "Fails registration with conflicting plugin name during initial traverse after plugin watcher restarts",
                        returnErr:   fmt.Errorf("conflicting plugin name"),
                },
        }

	callbackCount := 0
        w.AddHandler("example-plugin-type", func(name string, versions []string, sockPath string) error {
		glog.Infof("receives plugin watcher callback for test: %s\n", testCases[callbackCount].description)
                require.Equal(t, "example-plugin", name, "Plugin name mismatched!!")
                require.Equal(t, []string{"v1beta1", "v1beta2"}, versions, "Plugin version mismatched!!")
                // Verifies the grpcServer is ready to serve services.
                conn, err := grpc.Dial(socketPath, grpc.WithInsecure(), grpc.WithBlock(),
                        grpc.WithTimeout(10*time.Second),
                        grpc.WithDialer(func(addr string, timeout time.Duration) (net.Conn, error) {
                                return net.DialTimeout("unix", addr, timeout)
                        }))
                require.Nil(t, err)
                defer conn.Close()

		// The plugin handler should be able to use any listed service API version.
                v1beta1Client := v1beta1.NewExampleClient(conn)
                v1beta2Client := v1beta2.NewExampleClient(conn)

                // Tests v1beta1 GetExampleInfo
                _, err = v1beta1Client.GetExampleInfo(context.Background(), &v1beta1.ExampleRequest{})
                require.Nil(t, err)

                // Tests v1beta1 GetExampleInfo
                _, err = v1beta2Client.GetExampleInfo(context.Background(), &v1beta2.ExampleRequest{})
                require.Nil(t, err)
		ret := testCases[callbackCount].returnErr
		callbackCount++
                return ret
        })
        err = w.Start()
        require.NoError(t, err)
        defer w.Stop()

        p := NewTestExamplePlugin()
        go func() {
                p.Serve(socketPath)
        }()
	status := <-p.registrationStatus
	require.True(t, status.PluginRegistered)

	// Trying to start a plugin service at the same socket path should fail
	// with "bind: address already in use"
	err = p.Serve(socketPath)
	require.NotNil(t, err)

	// grpcServer.Stop() will remove the socket and starting plugin service
	// at the same path again should succeeds and trigger another callback.
        p.Stop()
	p = NewTestExamplePlugin()
        go func() {
                p.Serve(socketPath)
        }()
	status = <-p.registrationStatus
	require.True(t, status.PluginRegistered)

	// Starting another plugin with the same name got verification error.
	p2 := NewTestExamplePlugin()
	socketPath2 := socketDir + "/plugin2.sock"
        go func() {
                p2.Serve(socketPath2)
        }()
	status = <-p2.registrationStatus
	require.False(t, status.PluginRegistered)

	// Restarts plugin watcher should traverse the socket directory and issues a
	// callback for every existing socket.
	glog.Infof("restarting watcher 1\n")
	close(w.stopCh)
	w.stopCh = make(chan interface{})
	go func() {
		err = w.Start()
		glog.Infof("restarting watcher 2\n")
        	require.NoError(t, err)
	}()
	status = <-p.registrationStatus
	require.True(t, status.PluginRegistered)
	status = <-p2.registrationStatus
	require.False(t, status.PluginRegistered)
}
