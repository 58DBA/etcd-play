// Copyright 2016 CoreOS, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package proc

import (
	"fmt"
	"sync"

	"github.com/coreos/etcd/tools/functional-tester/etcd-agent/client"
)

type NodeWebRemoteClient struct {
	mu    sync.Mutex
	Flags *Flags
	Agent client.Agent

	active bool
}

func (nd *NodeWebRemoteClient) Endpoint() string {
	return nd.Flags.ExperimentalgRPCAddr
}

func (nd *NodeWebRemoteClient) StatusEndpoint() string {
	es := ""
	for k := range nd.Flags.ListenClientURLs {
		es = k
		break
	}
	return es // TODO: deprecate this v2 endpoint
}

func (nd *NodeWebRemoteClient) IsActive() bool {
	nd.mu.Lock()
	active := nd.active
	nd.mu.Unlock()
	return active
}

func (nd *NodeWebRemoteClient) Start() error {
	nd.mu.Lock()
	active := nd.active
	nd.mu.Unlock()
	if active {
		return fmt.Errorf("%s is already active", nd.Flags.Name)
	}

	flagSlice, err := nd.Flags.StringSlice()
	if err != nil {
		return err
	}
	if _, err := nd.Agent.Start(flagSlice...); err != nil {
		return err
	}
	nd.mu.Lock()
	nd.active = true
	nd.mu.Unlock()
	return nil
}

func (nd *NodeWebRemoteClient) Restart() error {
	nd.mu.Lock()
	active := nd.active
	nd.mu.Unlock()
	if active {
		return fmt.Errorf("%s is already active", nd.Flags.Name)
	}

	if _, err := nd.Agent.Restart(); err != nil {
		return err
	}
	nd.mu.Lock()
	nd.active = true
	nd.mu.Unlock()
	return nil
}

func (nd *NodeWebRemoteClient) Terminate() error {
	nd.mu.Lock()
	active := nd.active
	nd.mu.Unlock()
	if !active {
		return fmt.Errorf("%s is already terminated", nd.Flags.Name)
	}
	if err := nd.Agent.Stop(); err != nil {
		return err
	}
	nd.mu.Lock()
	nd.active = false
	nd.mu.Unlock()
	return nil
}

func (nd *NodeWebRemoteClient) Clean() error {
	if err := nd.Agent.Cleanup(); err != nil {
		return err
	}
	return nil
}