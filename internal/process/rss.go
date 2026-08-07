package process

import (
	"os"
	"strconv"
	"strings"
)

// ResidentBytes returns how much memory the upstream process actually has
// resident, or 0 when it cannot be read (not running, or a platform without
// /proc).
//
// This exists because host-level "available memory" cannot measure a hybrid
// placement. llama.cpp mmaps weights, so CPU-resident experts are file-backed
// page cache: they are reclaimable, so MemAvailable barely moves, and the
// host-delta reading for ~49 GB of resident experts came out at 700 MB. The
// process's own RSS counts those mapped pages, which is the number that
// actually describes what the model is using.
func (p *ProcessCommand) ResidentBytes() int64 {
	pid := p.currentPID()
	if pid <= 0 {
		return 0
	}
	return residentBytesForPID(pid)
}

// currentPID returns the running upstream process's PID, or 0.
func (p *ProcessCommand) currentPID() int {
	if pid := p.pid.Load(); pid > 0 {
		return int(pid)
	}
	return 0
}

// residentBytesForPID reads VmRSS from /proc/<pid>/status. VmRSS includes
// resident file-backed pages, which is exactly what mmap'd weights are.
func residentBytesForPID(pid int) int64 {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0
	}
	return parseVmRSS(string(data))
}

// parseVmRSS extracts the VmRSS line (always reported in kB) as bytes.
func parseVmRSS(status string) int64 {
	for _, line := range strings.Split(status, "\n") {
		rest, ok := strings.CutPrefix(line, "VmRSS:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0
		}
		return kb * 1024
	}
	return 0
}
