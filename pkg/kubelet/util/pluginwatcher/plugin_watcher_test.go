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
	"io/ioutil"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/context"
	"google.golang.org/grpc"

	watcherapi "k8s.io/kubernetes/pkg/kubelet/apis/pluginregistration/v1alpha"
	v1beta1 "k8s.io/kubernetes/pkg/kubelet/util/pluginwatcher/example_plugin_apis/v1beta1"
	v1beta2 "k8s.io/kubernetes/pkg/kubelet/util/pluginwatcher/example_plugin_apis/v1beta2"
)

func TestExamplePlugin(t *testing.T) {
	socketDir, err := ioutil.TempDir("", "device_plugin")
	require.NoError(t, err)
	socketPath := socketDir + "/plugin.sock"

	w := NewWatcher(socketDir)
	w.AddHandler("example-plugin-type", func(name string, versions []string, sockPath string) error {
		require.Equal(t, "example-plugin", name, "Plugin name mismatched!!")
		return nil
	})
	err = w.Start()
	require.NoError(t, err)
	defer w.Stop()

	p := NewExamplePlugin()
	go func() {
		p.Serve(socketPath)
	}()

	// Verifies the grpcServer is ready to serve services.
	conn, err := grpc.Dial(socketPath, grpc.WithInsecure(), grpc.WithBlock(),
		grpc.WithTimeout(10*time.Second),
		grpc.WithDialer(func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("unix", addr, timeout)
		}))
	require.Nil(t, err)
	defer conn.Close()

	watcherClient := watcherapi.NewRegistrationClient(conn)
	require.NotNil(t, watcherClient)
	v1beta1Client := v1beta1.NewExampleClient(conn)
	v1beta2Client := v1beta2.NewExampleClient(conn)

	// Tests watcher api GetInfo

	// Tests v1beta1 GetExampleInfo
	_, err = v1beta1Client.GetExampleInfo(context.Background(), &v1beta1.ExampleRequest{})
	require.Nil(t, err)

	// Tests v1beta1 GetExampleInfo
	_, err = v1beta2Client.GetExampleInfo(context.Background(), &v1beta2.ExampleRequest{})
	require.Nil(t, err)
}
