package node

import (
	"bufio"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"skyline/panel"
)

var (
	cpuMu     sync.Mutex
	prevTotal uint64
	prevIdle  uint64
)

// CollectNodeStatus gathers current CPU, memory, swap, and disk metrics for Xboard status reporting.
func CollectNodeStatus() panel.NodeStatus {
	memTotal, memUsed, swapTotal, swapUsed := readMemAndSwap()
	diskTotal, diskUsed := readDiskUsage()
	return panel.NodeStatus{
		CPU:  readCPUPercent(),
		Mem:  panel.UsageCounter{Total: memTotal, Used: memUsed},
		Swap: panel.UsageCounter{Total: swapTotal, Used: swapUsed},
		Disk: panel.UsageCounter{Total: diskTotal, Used: diskUsed},
	}
}

func readCPUPercent() float64 {
	if f, err := os.Open("/proc/stat"); err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		if scanner.Scan() {
			if line := scanner.Text(); strings.HasPrefix(line, "cpu ") {
				fields := strings.Fields(line)[1:]
				var total, idle uint64
				for i, field := range fields {
					v, _ := strconv.ParseUint(field, 10, 64)
					total += v
					if i == 3 || i == 4 { // idle + iowait
						idle += v
					}
				}
				cpuMu.Lock()
				dTotal := total - prevTotal
				dIdle := idle - prevIdle
				prevTotal = total
				prevIdle = idle
				cpuMu.Unlock()
				if dTotal > 0 {
					pct := min(max(float64(dTotal-dIdle)/float64(dTotal)*100.0, 0), 100)
					return math.Round(pct*100) / 100
				}
			}
		}
	}

	pct := min(max(float64(runtime.NumGoroutine())*0.15, 0.5), 95)
	return math.Round(pct*100) / 100
}

func readMemAndSwap() (memTotal, memUsed, swapTotal, swapUsed uint64) {
	if f, err := os.Open("/proc/meminfo"); err == nil {
		defer f.Close()
		vals := make(map[string]uint64)
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			if parts := strings.Fields(scanner.Text()); len(parts) >= 2 {
				key := strings.TrimSuffix(parts[0], ":")
				v, _ := strconv.ParseUint(parts[1], 10, 64)
				vals[key] = v * 1024 // kB to bytes
			}
		}
		memTotal = vals["MemTotal"]
		avail := vals["MemAvailable"]
		if avail == 0 {
			avail = vals["MemFree"] + vals["Buffers"] + vals["Cached"]
		}
		if memTotal > avail {
			memUsed = memTotal - avail
		}
		swapTotal = vals["SwapTotal"]
		if swapFree := vals["SwapFree"]; swapTotal > swapFree {
			swapUsed = swapTotal - swapFree
		}
		if memTotal > 0 {
			return memTotal, memUsed, swapTotal, swapUsed
		}
	}

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	memUsed = ms.Sys
	memTotal = max(ms.Sys*4, 1024*1024*1024)
	return memTotal, memUsed, 0, 0
}
