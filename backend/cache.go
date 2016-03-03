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

package backend

import (
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/context"

	"github.com/coreos/etcd-play/proc"
	"github.com/gorilla/websocket"
	"github.com/gyuho/psn/ss"
)

type (
	userData struct {
		upgrader *websocket.Upgrader

		startTime       time.Time
		lastRequestTime time.Time
		requestCount    int

		selectedNodeName  string
		selectedOperation string

		lastKey   string
		lastValue string

		keyHistory []string
	}

	cache struct {
		mu      sync.Mutex
		cluster proc.Cluster
		users   map[string]*userData
	}

	status struct {
		mu             sync.Mutex
		activeUserList string
		nameToStatus   map[string]proc.ServerStatus
	}
)

var (
	globalPorts = ss.NewPorts()
	globalCache = &cache{
		cluster: nil,
		users:   make(map[string]*userData),
	}
	globalStatus = &status{
		activeUserList: "",
		nameToStatus:   make(map[string]proc.ServerStatus),
	}

	uptimeScale = time.Second
	startTime   = time.Now().Round(uptimeScale)
)

// initGlobalData must be called at the beginning of 'web' command.
func initGlobalData() {
	if globalFlags.LinuxAutoPort {
		globalPorts.Refresh()
		go func() {
			for {
				select {
				case <-time.After(globalFlags.LinuxIntervalPortRefresh):
					globalPorts.Refresh()
				}
			}
		}()
	}

	globalCache.mu.Lock()
	if globalCache.users == nil {
		globalCache.users = make(map[string]*userData)
	}
	globalCache.mu.Unlock()

	// keep pulling cluster status
	go func() {
		for {
			if globalCache.clusterActive() {
				globalCache.mu.Lock()
				userN := len(globalCache.users)
				globalCache.mu.Unlock()

				if userN > 0 {
					users := []string{}
					globalCache.mu.Lock()
					cn := 0
					for u := range globalCache.users {
						bts := []byte(u)
						bts[2] = 'x' // mask IP addresses
						bts[3] = 'x'
						bts[4] = 'x'
						bts[5] = 'x'
						bts[6] = 'x'
						bts[7] = 'x'
						bts[8] = 'x'
						bs := string(bts)
						if len(bs) > 23 {
							bs = bs[:23] + "..."
						}
						users = append(users, bs)
						cn++
						if cn > 20 {
							break
						}
					}
					globalCache.mu.Unlock()
					sort.Strings(users)
					if len(users) > 20 {
						users = append(users, "...more")
					}
					us := strings.Join(users, "<br>")

					st, err := globalCache.cluster.Status()
					if err != nil {
						log.Println(err)
					}
					globalStatus.mu.Lock()
					globalStatus.activeUserList = us
					globalStatus.nameToStatus = st
					globalStatus.mu.Unlock()
				}
			}
			time.Sleep(time.Second)
		}
	}()

	// clean up users that started more than 1-hour ago
	go func() {
		for {
			now := time.Now()
			globalCache.mu.Lock()
			for userID, v := range globalCache.users {
				sub := now.Sub(v.startTime)
				if sub > time.Hour {
					delete(globalCache.users, userID)
				}
			}
			globalCache.mu.Unlock()

			time.Sleep(time.Hour)
		}
	}()

	// revive cluster in case somebody killed all
	go func() {
		for {
			time.Sleep(globalFlags.ReviveInterval)
			if !globalCache.clusterActive() {
				continue
			}
			globalCache.mu.Lock()
			if err := globalCache.cluster.Revive(); err != nil {
				log.Println(err)
			}
			globalCache.mu.Unlock()
		}
	}()
}

func withCache(h ContextHandler) ContextHandler {
	return ContextHandlerFunc(func(ctx context.Context, w http.ResponseWriter, req *http.Request) error {
		userID := getUserID(req)
		ctx = context.WithValue(ctx, userKey, &userID)

		globalCache.mu.Lock()
		if _, ok := globalCache.users[userID]; !ok {
			globalCache.users[userID] = &userData{
				upgrader:        &websocket.Upgrader{},
				startTime:       time.Now(),
				lastRequestTime: time.Time{},
				requestCount:    0,
				keyHistory: []string{
					`TYPE_YOUR_KEY`,
					`sample_key`,
				},
			}
		}
		globalCache.mu.Unlock()

		// (X) this will deadlock
		// defer globalCache.mu.Unlock()
		return h.ServeHTTPContext(ctx, w, req)
	})
}

// checkCluster returns the cluster if the cluster is active.
func (s *cache) clusterActive() bool {
	s.mu.Lock()
	clu := s.cluster
	s.mu.Unlock()
	return clu != nil
}

func (s *cache) okToRequest(userID string) bool {
	// allow maximum 5 requests per second
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.users[userID]
	if !ok {
		return false
	}
	v.requestCount++
	if v.requestCount == 1 {
		v.lastRequestTime = time.Now()
	}
	if v.requestCount < 5 {
		return true
	}
	sub := time.Now().Sub(v.lastRequestTime)
	if sub > time.Second {
		v.lastRequestTime = time.Now()
		v.requestCount = 0
		return true
	}
	return false
}

func getWelcomeMsg() string {
	return boldHTMLMsg("Hello World! Welcome to etcd!") + fmt.Sprintf(`<br>
- You've joined an <a href="https://github.com/coreos/etcd" target="_blank"><b>etcd</b></a> cluster <i>with %d other user(s) now</i>.<br>
- This is a <b>real</b> <a href="https://github.com/coreos/etcd" target="_blank"><b>etcd</b></a> cluster of 5 nodes, deployed in cloud environment <font color="blue"><i>(not a fake or simulator!)</i></font>.<br>
- <a href="https://github.com/coreos/etcd" target="_blank"><b>etcd</b></a> is distributed reliable key-value store for the most critical data of a distributed system.<br>
- Using <a href="https://raft.github.io" target="_blank">Raft</a>, <a href="https://github.com/coreos/etcd" target="_blank"><b>etcd</b></a> gracefully handles network partitions and machine failures, even <font color='red'>leader failures</font>.<br>
- Tutorials and source code can be found at <a href="https://github.com/coreos/etcd-play" target="_blank"><b>coreos/etcd-play</b></a>.<br>
- This runs <b>master branch of <a href="https://github.com/coreos/etcd" target="_blank">etcd</a></b>. For any issues or questions, please report at <i><b><a href="https://github.com/coreos/etcd-play/issues/new" target="_blank">issues</a></b></i>.<br>
- Please click <font color='#0000A0'>circle(node)</font> for more node information (<font color='green'>green</font> is leader, <font color='blue'>blue</font> is follower).<br>
- <font color='red'>Kill</font> to stop node(even the <font color='green'><b>leader</b></font>). <font color='red'>Restart</font> to recover node.<br>
- <font color='blue'>Hash</font> shows how <b>etcd</b>, <i>as a distributed database</i>, <b>keeps its data consistent</b>.<br>
- Select <b>any endpoint</b><i>(etcd1, etcd2, ...)</i> to PUT, GET, DELETE, and then click <b>Submit</b>.<br>
<br>
<i>Note: Request logs are streamed based on your IP and user agent. So if you have multiple<br>
web browsers running at the same time, logs might be shown only in one of them.</i><br>
`, len(globalCache.users)-1)
}
