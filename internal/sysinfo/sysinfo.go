// Package sysinfo reads CPU and memory utilization from /proc.
// Values are cached and updated at most once per second from a
// background goroutine — the render loop just reads the cache.
package sysinfo

import (
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var (
	cpuPct  atomic.Value // float64
	memPct  atomic.Value // float64
	swpPct  atomic.Value // float64
	started bool
)

// Start begins the background /proc reader (1 Hz).
func Start() {
	if started {
		return
	}
	started = true
	cpuPct.Store(0.0)
	memPct.Store(0.0)
	swpPct.Store(0.0)
	go loop()
}

func loop() {
	var lastIdle, lastTotal uint64
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for range tick.C {
		// CPU: /proc/stat first line
		if b, err := os.ReadFile("/proc/stat"); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, "cpu ") {
					f := strings.Fields(line)[1:]
					var total, idle uint64
					for i, v := range f {
						n, _ := strconv.ParseUint(v, 10, 64)
						total += n
						if i == 3 || i == 4 {
							idle += n
						}
					}
					if total > lastTotal {
						pct := 100 * float64(total-lastTotal-(idle-lastIdle)) / float64(total-lastTotal)
						cpuPct.Store(pct)
					}
					lastIdle, lastTotal = idle, total
					break
				}
			}
		}
		// Memory + swap
		if b, err := os.ReadFile("/proc/meminfo"); err == nil {
			var memT, memA, swpT, swpF uint64
			for _, line := range strings.Split(string(b), "\n") {
				f := strings.Fields(line)
				if len(f) < 2 {
					continue
				}
				v, _ := strconv.ParseUint(f[1], 10, 64)
				switch {
				case strings.HasPrefix(line, "MemTotal:"):
					memT = v
				case strings.HasPrefix(line, "MemAvailable:"):
					memA = v
				case strings.HasPrefix(line, "SwapTotal:"):
					swpT = v
				case strings.HasPrefix(line, "SwapFree:"):
					swpF = v
				}
			}
			if memT > 0 {
				memPct.Store(100 * float64(memT-memA) / float64(memT))
			}
			if swpT > 0 {
				swpPct.Store(100 * float64(swpT-swpF) / float64(swpT))
			}
		}
	}
}

// Snapshot returns the cached CPU%, MEM%, SWAP%.
func Snapshot() (cpu, mem, swap float64) {
	c, _ := cpuPct.Load().(float64)
	m, _ := memPct.Load().(float64)
	s, _ := swpPct.Load().(float64)
	return c, m, s
}
