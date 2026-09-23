// Package sysinfo reads CPU and memory utilization from /proc for the
// on-screen diagnostic display.
package sysinfo

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	mu       sync.Mutex
	lastCPU  [10]uint64 // idle + total per CPU
	curUtil  float64    // 0-100
	curMemPc float64
	curSwpPc float64
	lastRead time.Time
)

// Read updates the cached values (call every 1-2 seconds; reading
// /proc/stat needs a delta between two reads to compute utilization).
func Read() {
	mu.Lock()
	defer mu.Unlock()
	if time.Since(lastRead) < time.Second {
		return
	}
	lastRead = time.Now()

	// CPU: /proc/stat "cpu  user nice system idle iowait irq softirq steal"
	b, err := os.ReadFile("/proc/stat")
	if err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "cpu ") {
				fields := strings.Fields(line)[1:]
				var total, idle uint64
				for i, f := range fields {
					v, _ := strconv.ParseUint(f, 10, 64)
					total += v
					if i == 3 || i == 4 { // idle + iowait
						idle += v
					}
				}
				prevTotal := lastCPU[0] + lastCPU[1]
				prevIdle := lastCPU[0]
				if total > prevTotal {
					util := 100 * float64(total-prevTotal-(idle-prevIdle)) / float64(total-prevTotal)
					curUtil = util
				}
				lastCPU[0] = idle
				lastCPU[1] = total
				break
			}
		}
	}

	// Memory + swap: /proc/meminfo
	mb, err := os.ReadFile("/proc/meminfo")
	if err == nil {
		var memTotal, memAvail, swpTotal, swpFree uint64
		for _, line := range strings.Split(string(mb), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			v, _ := strconv.ParseUint(fields[1], 10, 64)
			switch {
			case strings.HasPrefix(line, "MemTotal:"):
				memTotal = v
			case strings.HasPrefix(line, "MemAvailable:"):
				memAvail = v
			case strings.HasPrefix(line, "SwapTotal:"):
				swpTotal = v
			case strings.HasPrefix(line, "SwapFree:"):
				swpFree = v
			}
		}
		if memTotal > 0 {
			curMemPc = 100 * float64(memTotal-memAvail) / float64(memTotal)
		}
		if swpTotal > 0 {
			curSwpPc = 100 * float64(swpTotal-swpFree) / float64(swpTotal)
		}
	}
}

// Snapshot returns the latest readings.
func Snapshot() (cpu, mem, swap float64) {
	mu.Lock()
	defer mu.Unlock()
	return curUtil, curMemPc, curSwpPc
}
