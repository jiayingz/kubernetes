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
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/golang/glog"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	watcherapi "k8s.io/kubernetes/pkg/kubelet/apis/pluginregistration/v1alpha"
	utilfs "k8s.io/kubernetes/pkg/util/filesystem"
)

// RegisterCallbackFn is the type of the callback function that handlers will provide
type RegisterCallbackFn func(pluginName string, versions []string, socketPath string) error

// Watcher is the plugin watcher
type Watcher struct {
	path     string
	handlers map[string]RegisterCallbackFn
	stopCh   chan interface{}
	fs       utilfs.Filesystem
}

// NewWatcher provides a new watcher
func NewWatcher(sockDir string) Watcher {
	return Watcher{
		path:     sockDir,
		handlers: make(map[string]RegisterCallbackFn),
		stopCh:   make(chan interface{}),
		fs:       &utilfs.DefaultFs{},
	}
}

// AddHandler registers a callback to be invoked for a particular type of plugin
func (w *Watcher) AddHandler(handlerType string, handlerCbkFn RegisterCallbackFn) {
	w.handlers[handlerType] = handlerCbkFn
}

// Creates the plugin directory, if it doesn't already exist.
func (w *Watcher) createPluginDir() error {
	if _, err := w.fs.Stat(w.path); os.IsNotExist(err) {
		glog.Infof("Plugin directory at %s does not exist. Recreating.", w.path)
		err := w.fs.MkdirAll(w.path, 0755)
		if err != nil {
			return fmt.Errorf("error (re-)creating driver directory: %s", err)
		}
	}
	return nil
}

// Walks through the plugin directory to discover any existing plugin sockets.
func (w *Watcher) traversePluginDir() error {
	files, err := w.fs.ReadDir(w.path)
	if err != nil {
		return fmt.Errorf("error reading the plugin directory: %v", err)
	}
	for _, f := range files {
		// Currently only supports flat fs namespace under the plugin directory.
		// TODO: adds support for hierachical fs namespace.
		if !f.IsDir() && filepath.Base(f.Name())[0] != '.' {
			w.registerPlugin(path.Join(w.path, f.Name()))
		}
	}
	return nil
}

func (w *Watcher) init() error {
	if err := w.createPluginDir(); err != nil {
		return err
	}
	return w.traversePluginDir()
}

func (w *Watcher) registerPlugin(socketPath string) error {
	glog.V(2).Infof("registerPlugin called for socketPath: %s", socketPath)
	client, conn, err := dial(socketPath)
	if err != nil {
		glog.Warningf("Dial failed at socket %s, err: %v", socketPath, err)
		return err
	}
	defer conn.Close()
	stream, err := client.GetInfo(context.Background())
	if err != nil {
		glog.Errorf("Failed to start GetInfo grpc stream at socket %s, err: %v", socketPath, err)
		return err
	}
	defer stream.CloseSend()
	rqst, err := stream.Recv()
	if err != nil {
		glog.Errorf("Failed to receive stream at socket %s, err: %v", socketPath, err)
		return err
	}
	resp := new(watcherapi.RegistrationStatus)
	if handlerCbkFn, ok := w.handlers[rqst.Type]; ok {
		var versions []string
		for _, version := range rqst.Version {
			versions = append(versions, version)
		}
		// calls handler callback to verify registration request
		if verifyErr := handlerCbkFn(rqst.Name, versions, socketPath); verifyErr != nil {
			resp.PluginRegistered = false
			resp.Error = fmt.Sprintf("Plugin registration failed with err: %v", verifyErr)
		} else {
			resp.PluginRegistered = true
		}
	} else {
		resp.PluginRegistered = false
		resp.Error = fmt.Sprintf("No handler found registered for plugin type: %s, socket: %s", rqst.Type, socketPath)
	}
	if err := stream.Send(resp); err != nil {
		glog.Errorf("Failed to send registration status at socket %s, err: %v", socketPath, err)
		return err
	}

	for {
		_, err := stream.Recv()
		if err == io.EOF {
			break
		} else if err != nil {
			glog.Errorf("Recv failed. err: %v", err)
			return err
		}
	}

	if !resp.PluginRegistered {
		glog.Errorf("Registration failed for plugin type: %s, name: %s, socket: %s, err: %s", rqst.Type, rqst.Name, socketPath, resp.Error)
		return fmt.Errorf("%s", resp.Error)
	}
	glog.Infof("Successfully registered plugin for plugin type: %s, name: %s, socket: %s", rqst.Type, rqst.Name, socketPath)
	return nil
}

// Start watches for the creation of plugin sockets at the path
func (w *Watcher) Start() error {
	glog.V(2).Infof("Plugin Watcher Start at %s", w.path)

	// Creating the directory to be watched if it doesn't exist yet,
	// and walks through the directory to discover the existing plugins.
	if err := w.init(); err != nil {
		return err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to start plugin watcher, err: %v", err)
	}

	err = watcher.Add(w.path)
	if err != nil {
		watcher.Close()
		return fmt.Errorf("failed to start plugin watcher, err: %v", err)
	}

	go func() {
		for {
			select {
			case event := <-watcher.Events:
				if event.Op&fsnotify.Create == fsnotify.Create {
					w.registerPlugin(event.Name)
				}
				continue
			case <-w.stopCh:
				watcher.Close()
				break
			}
		}
	}()
	return nil
}

// Stop stops probing the creation of plugin sockets at the path
func (w *Watcher) Stop() error {
	close(w.stopCh)
	return os.RemoveAll(w.path)
}

// Dial establishes the gRPC communication with the picked up plugin socket. https://godoc.org/google.golang.org/grpc#Dial
func dial(unixSocketPath string) (watcherapi.RegistrationClient, *grpc.ClientConn, error) {
	c, err := grpc.Dial(unixSocketPath, grpc.WithInsecure(), grpc.WithBlock(),
		grpc.WithTimeout(10*time.Second),
		grpc.WithDialer(func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("unix", addr, timeout)
		}),
	)

	if err != nil {
		return nil, nil, fmt.Errorf("failed to dial socket %s, err: %v", unixSocketPath, err)
	}

	return watcherapi.NewRegistrationClient(c), c, nil
}
