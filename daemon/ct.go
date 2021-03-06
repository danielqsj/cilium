// Copyright 2016-2017 Authors of Cilium
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

package main

import (
	"fmt"
	"strconv"
	"time"

	"github.com/cilium/cilium/pkg/bpf"
	"github.com/cilium/cilium/pkg/endpoint"
	"github.com/cilium/cilium/pkg/maps/ctmap"

	log "github.com/Sirupsen/logrus"
)

const (
	// GcInterval is the garbage collection interval.
	GcInterval int = 10
)

func runGC(e *endpoint.Endpoint, isLocal, isIPv6 bool) {
	var file string
	var mapType string
	// TODO: We need to optimize this a bit in future, so we traverse
	// the global table less often.

	// Use local or global conntrack maps depending on configuration settings.
	if isLocal {
		if isIPv6 {
			mapType = ctmap.MapName6
		} else {
			mapType = ctmap.MapName4
		}
		file = bpf.MapPath(mapType + strconv.Itoa(int(e.ID)))
	} else {
		if isIPv6 {
			mapType = ctmap.MapName6Global
		} else {
			mapType = ctmap.MapName4Global
		}
		file = bpf.MapPath(mapType)
	}

	m, err := bpf.OpenMap(file)
	if err != nil {
		log.Warningf("Unable to open map %s: %s", file, err)
		e.LogStatus(endpoint.BPF, endpoint.Warning, fmt.Sprintf("Unable to open CT map %s: %s", file, err))
		return
	}
	defer m.Close()

	// If LRUHashtable, no need to garbage collect as LRUHashtable cleans itself up.
	if m.MapInfo.MapType == bpf.MapTypeLRUHash {
		return
	}

	deleted := ctmap.GC(m, mapType)

	if deleted > 0 {
		log.Debugf("Deleted %d entries from map %s", deleted, file)
	}
}

// EnableConntrackGC enables the connection tracking garbage collection.
func (d *Daemon) EnableConntrackGC() {
	go func() {
		seenGlobal := false
		for {
			sleepTime := time.Duration(GcInterval) * time.Second

			d.endpointsMU.RLock()

			for k := range d.endpoints {
				e := d.endpoints[k]
				e.Mutex.RLock()

				if e.Consumable == nil {
					e.Mutex.RUnlock()
					continue
				}

				// Only process global CT once per round.
				// We don't really care about which EP
				// triggers the traversal as long as we do
				// traverse it eventually. Update/delete
				// combo only serialized done from here,
				// so no extra mutex for global CT needed
				// right now. We still need to traverse
				// other EPs since some may not be part
				// of the global CT, but have a local one.
				isLocal := e.Opts.IsEnabled(endpoint.OptionConntrackLocal)
				if isLocal == false {
					if seenGlobal == true {
						e.Mutex.RUnlock()
						continue
					}
					seenGlobal = true
				}

				e.Mutex.RUnlock()
				// We can unlock the endpoint mutex sense
				// in runGC it will be locked as needed.
				runGC(e, isLocal, true)
				if !d.conf.IPv4Disabled {
					runGC(e, isLocal, false)
				}
			}

			d.endpointsMU.RUnlock()
			time.Sleep(sleepTime)
			seenGlobal = false
		}
	}()
}
