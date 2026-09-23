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
	memUsed atomic.Value // float64 MiB
	memTot  atomic.Value // float64 MiB
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
	memUsed.Store(0.0)
	memTot.Store(0.0)
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
		// Thermal + battery (sysfs nodes vanish on dev PCs — fine).
		cpuT, gpuT, veT, ddrT := readThermal()
		bt, bp, bv, bs := readBattery()
		sensors.Store(Sensors{CPUTemp: cpuT, GPUTemp: gpuT, VETemp: veT, DDRTemp: ddrT, BattTemp: bt, BattPct: bp, BattVolt: bv, BattStatus: bs})
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
				memUsed.Store(float64(memT-memA) / 1024)
				memTot.Store(float64(memT) / 1024)
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

// Sensors holds the thermal-zone and battery readings (empty/zero
// when the sysfs nodes are absent).
type Sensors struct {
	CPUTemp, GPUTemp, VETemp, DDRTemp float64 // °C
	BattTemp                          float64 // °C
	BattPct                           int     // %
	BattVolt                          float64 // V
	BattStatus                        string  // Charging/Discharging/…
}

var sensors atomic.Value // Sensors

// SensorSnapshot returns the cached thermal + battery readings.
func SensorSnapshot() Sensors {
	s, _ := sensors.Load().(Sensors)
	return s
}

// readThermal walks /sys/class/thermal and picks the cpu/gpu/ve/ddr
// zones by type name (millidegrees → °C).
func readThermal() (cpuT, gpuT, veT, ddrT float64) {
	entries, err := os.ReadDir("/sys/class/thermal")
	if err != nil {
		return
	}
	for _, e := range entries {
		t, err := os.ReadFile("/sys/class/thermal/" + e.Name() + "/type")
		if err != nil {
			continue
		}
		typ := strings.TrimSpace(string(t))
		v, err := os.ReadFile("/sys/class/thermal/" + e.Name() + "/temp")
		if err != nil {
			continue
		}
		milli, err := strconv.ParseInt(strings.TrimSpace(string(v)), 10, 64)
		if err != nil {
			continue
		}
		c := float64(milli) / 1000
		switch {
		case strings.Contains(typ, "cpu"):
			cpuT = c
		case strings.Contains(typ, "gpu"):
			gpuT = c
		case strings.Contains(typ, "ve"):
			veT = c
		case strings.Contains(typ, "ddr"):
			ddrT = c
		}
	}
	return
}

// readBattery finds the Battery power supply and reads temp (tenths
// of °C), capacity %, voltage_now (µV) and status.
func readBattery() (temp float64, pct int, volt float64, status string) {
	entries, err := os.ReadDir("/sys/class/power_supply")
	if err != nil {
		return
	}
	for _, e := range entries {
		base := "/sys/class/power_supply/" + e.Name()
		t, err := os.ReadFile(base + "/type")
		if err != nil || strings.TrimSpace(string(t)) != "Battery" {
			continue
		}
		if b, err := os.ReadFile(base + "/temp"); err == nil {
			if n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil {
				temp = float64(n) / 10
			}
		}
		if b, err := os.ReadFile(base + "/capacity"); err == nil {
			if n, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				pct = n
			}
		}
		if b, err := os.ReadFile(base + "/voltage_now"); err == nil {
			if n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil {
				volt = float64(n) / 1e6
			}
		}
		if b, err := os.ReadFile(base + "/status"); err == nil {
			status = strings.TrimSpace(string(b))
		}
		break
	}
	return
}

// MemAbsolute returns the cached used/total memory in MiB.
func MemAbsolute() (usedMiB, totalMiB float64) {
	u, _ := memUsed.Load().(float64)
	t, _ := memTot.Load().(float64)
	return u, t
}
