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
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net/url"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// NodeWebLocal represents an etcd node in local web host.
type NodeWebLocal struct {
	pmu                *sync.Mutex // inherit from Cluster
	pmaxProcNameLength *int
	colorIdx           int

	liveLog      bool
	sharedStream chan string // inherit from Cluster (no need pointer)

	ProgramPath string
	Flags       *Flags

	cmd *exec.Cmd
	PID int

	active bool

	limitInterval  time.Duration
	lastTerminated time.Time
	lastRestarted  time.Time
}

func (nd *NodeWebLocal) Write(p []byte) (int, error) {
	buf := bytes.NewBuffer(p)
	wrote := 0
	for {
		line, err := buf.ReadBytes('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			return wrote, err
		}
		if len(line) > 1 {
			format := fmt.Sprintf("%%%ds | ", *(nd.pmaxProcNameLength))
			format = fmt.Sprintf(`<b><font color="%s">`, colorsToHTML[colorsTerminal[nd.colorIdx]]) + format + "</font>" + "%s</b>"
			nd.sharedStream <- fmt.Sprintf(format, nd.Flags.Name, line)
			wrote += len(line)
		}
	}

	return len(p), nil
}

func (nd *NodeWebLocal) Endpoint() string {
	es := ""
	for k := range nd.Flags.ListenClientURLs {
		es = k
		break
	}
	s, _ := url.Parse(es)
	return s.Host
}

func (nd *NodeWebLocal) StatusEndpoint() string {
	es := ""
	for k := range nd.Flags.ListenClientURLs {
		es = k
		break
	}
	return es
}

func (nd *NodeWebLocal) IsActive() bool {
	nd.pmu.Lock()
	active := nd.active
	nd.pmu.Unlock()
	return active
}

func (nd *NodeWebLocal) Start() error {
	defer func() {
		if err := recover(); err != nil {
			nd.sharedStream <- fmt.Sprintf("Start %s: panic (%v)\n", nd.Flags.Name, err)
		}
	}()
	nd.pmu.Lock()
	active := nd.active
	nd.pmu.Unlock()
	if active {
		return fmt.Errorf("%s is already running or requested to restart", nd.Flags.Name)
	}

	shell := os.Getenv("SHELL")
	if len(shell) == 0 {
		shell = "sh"
	}
	nd.pmu.Lock()
	flagString, err := nd.Flags.String()
	if err != nil {
		return err
	}
	args := []string{shell, "-c", nd.ProgramPath + " " + flagString}
	nd.pmu.Unlock()

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = nil
	cmd.Stdout = nd
	cmd.Stderr = nd
	if !nd.liveLog {
		cmd.Stdout = ioutil.Discard
		cmd.Stderr = ioutil.Discard
	}

	nd.sharedStream <- fmt.Sprintf("Start %s\n", nd.Flags.Name)
	if err := cmd.Start(); err != nil {
		return err
	}

	nd.pmu.Lock()
	nd.cmd = cmd
	nd.PID = cmd.Process.Pid
	nd.active = true
	nd.pmu.Unlock()

	go func() {
		if err := cmd.Wait(); err != nil {
			nd.sharedStream <- fmt.Sprintf("Start(%s) cmd.Wait returned %v\n", nd.Flags.Name, err)
			return
		}
		nd.sharedStream <- fmt.Sprintf("Exiting %s\n", nd.Flags.Name)
	}()
	return nil
}

func (nd *NodeWebLocal) Restart() error {
	defer func() {
		if err := recover(); err != nil {
			nd.sharedStream <- fmt.Sprintf("Restart %s: panic (%v)\n", nd.Flags.Name, err)
		}
	}()

	nd.pmu.Lock()
	active := nd.active
	lastTerminated := nd.lastTerminated
	lastRestarted := nd.lastRestarted
	nd.pmu.Unlock()
	if active {
		return fmt.Errorf("%s is already running or requested to restart", nd.Flags.Name)
	}

	// restart, 2nd restart term should be more than limit interval
	sub := time.Now().Sub(lastRestarted)
	if sub < nd.limitInterval {
		return fmt.Errorf("Somebody restarted the same node (only %v ago)! Retry in %v!", sub, nd.limitInterval)
	}
	// terminate, and immediate restart term should be more than limit interval
	subt := time.Now().Sub(lastTerminated)
	if subt < nd.limitInterval {
		return fmt.Errorf("Somebody terminated the node (only %v ago)! Retry in %v!", subt, nd.limitInterval)
	}

	shell := os.Getenv("SHELL")
	if len(shell) == 0 {
		shell = "sh"
	}
	nd.pmu.Lock()
	nd.Flags.InitialClusterState = "existing"
	flagString, err := nd.Flags.String()
	if err != nil {
		return err
	}
	args := []string{shell, "-c", nd.ProgramPath + " " + flagString}
	nd.pmu.Unlock()

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = nil
	cmd.Stdout = nd
	cmd.Stderr = nd

	nd.sharedStream <- fmt.Sprintf("Restart %s\n", nd.Flags.Name)
	if err := cmd.Start(); err != nil {
		return err
	}

	nd.pmu.Lock()
	nd.cmd = cmd
	nd.PID = cmd.Process.Pid
	nd.lastRestarted = time.Now()
	nd.active = true
	nd.pmu.Unlock()

	go func() {
		if err := cmd.Wait(); err != nil {
			nd.sharedStream <- fmt.Sprintf("Restart(%s) cmd.Wait returned %v\n", nd.Flags.Name, err)
			return
		}
		nd.sharedStream <- fmt.Sprintf("Exiting %s\n", nd.Flags.Name)
	}()
	return nil
}

func (nd *NodeWebLocal) Terminate() error {
	defer func() {
		if err := recover(); err != nil {
			nd.sharedStream <- fmt.Sprintf("Terminate %s: panic (%v)\n", nd.Flags.Name, err)
		}
	}()

	nd.pmu.Lock()
	active := nd.active
	lastTerminated := nd.lastTerminated
	lastRestarted := nd.lastRestarted
	nd.pmu.Unlock()
	if !active {
		return fmt.Errorf("%s is already terminated or requested to terminate", nd.Flags.Name)
	}

	// terminate, 2nd terminate term should be more than limit interval
	sub := time.Now().Sub(lastTerminated)
	if sub < nd.limitInterval {
		return fmt.Errorf("Somebody terminated the same node (only %v ago)! Retry in %v!", sub, nd.limitInterval)
	}
	// restart, and immediate terminate term should be more than limit interval
	subt := time.Now().Sub(lastRestarted)
	if subt < nd.limitInterval {
		return fmt.Errorf("Somebody restarted the node (only %v ago)! Retry in %v!", subt, nd.limitInterval)
	}

	nd.sharedStream <- fmt.Sprintf("Terminate %s [PID: %d]\n", nd.Flags.Name, nd.PID)
	if err := syscall.Kill(nd.PID, syscall.SIGTERM); err != nil {
		return err
	}
	// if err := syscall.Kill(nd.PID, syscall.SIGKILL); err != nil {
	// 	return err
	// }

	nd.pmu.Lock()
	nd.lastTerminated = time.Now()
	nd.active = false
	nd.pmu.Unlock()

	return nil
}

func (nd *NodeWebLocal) Clean() error {
	defer func() {
		if err := recover(); err != nil {
			nd.sharedStream <- fmt.Sprintf("Clean %s: panic (%v)\n", nd.Flags.Name, err)
		}
	}()
	nd.pmu.Lock()
	nd.lastTerminated = time.Now()
	active := nd.active
	nd.pmu.Unlock()
	if active {
		return fmt.Errorf("%s is already running or requested to restart", nd.Flags.Name)
	}

	nd.sharedStream <- fmt.Sprintf("Clean %s (%s)\n", nd.Flags.Name, nd.Flags.DataDir)
	if err := os.RemoveAll(nd.Flags.DataDir); err != nil {
		return err
	}
	return nil
}
