package perf

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// cgroup v1 reports "unlimited" as a page-rounded max int64; anything this
// large is not a real limit.
const cgroupV1Unlimited = uint64(1) << 60

// memoryLimit describes the cgroup memory limit that applies to this process.
type memoryLimit struct {
	LimitBytes uint64
	UsageBytes uint64
	Source     string // "cgroup-v2" or "cgroup-v1"
}

// EffectiveMemoryLimit resolves the cgroup memory limit applying to the
// current process. ok is false when no limit applies (bare metal, unlimited
// cgroup, non-Linux, or unreadable cgroup files) — callers must fail open to
// raw /proc/meminfo figures.
func EffectiveMemoryLimit() (memoryLimit, bool) {
	if runtime.GOOS != "linux" {
		return memoryLimit{}, false
	}
	return effectiveMemoryLimitFrom("/proc/self/cgroup", "/sys/fs/cgroup")
}

// effectiveMemoryLimitFrom is the testable core: paths are injectable so unit
// tests run against a synthetic filesystem on any platform.
func effectiveMemoryLimitFrom(procSelfCgroup, cgroupRoot string) (memoryLimit, bool) {
	data, err := os.ReadFile(procSelfCgroup)
	if err != nil {
		return memoryLimit{}, false
	}
	if lim, ok := v2Limit(string(data), cgroupRoot); ok {
		return lim, true
	}
	if lim, ok := v1Limit(string(data), cgroupRoot); ok {
		return lim, true
	}
	return memoryLimit{}, false
}

// v2Limit walks the unified-hierarchy path from the process's own cgroup up
// to the visible root, taking the most restrictive memory.max. A parent can
// be tighter than the leaf, and in a cgroup namespace (Docker, LXC) the
// visible root is the container's own cgroup, whose memory.max carries the
// container limit.
func v2Limit(procSelfCgroup, cgroupRoot string) (memoryLimit, bool) {
	var ownPath string
	for _, line := range strings.Split(procSelfCgroup, "\n") {
		if rest, found := strings.CutPrefix(line, "0::"); found {
			ownPath = strings.TrimSpace(rest)
			break
		}
	}
	if ownPath == "" {
		return memoryLimit{}, false
	}

	limit := uint64(0)
	found := false
	for p := ownPath; ; p = filepath.Dir(p) {
		raw, err := os.ReadFile(filepath.Join(cgroupRoot, p, "memory.max"))
		if err == nil {
			if v, ok := parseLimitValue(string(raw)); ok {
				if !found || v < limit {
					limit = v
					found = true
				}
			}
		}
		if p == "/" || p == "." {
			break
		}
	}
	if !found {
		return memoryLimit{}, false
	}

	var usage uint64
	if raw, err := os.ReadFile(filepath.Join(cgroupRoot, ownPath, "memory.current")); err == nil {
		if v, ok := parseLimitValue(string(raw)); ok {
			usage = v
		}
	}
	return memoryLimit{LimitBytes: limit, UsageBytes: usage, Source: "cgroup-v2"}, true
}

// v1Limit reads the legacy memory controller's limit for this process.
func v1Limit(procSelfCgroup, cgroupRoot string) (memoryLimit, bool) {
	var ownPath string
	for _, line := range strings.Split(procSelfCgroup, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		for _, ctrl := range strings.Split(parts[1], ",") {
			if ctrl == "memory" {
				ownPath = strings.TrimSpace(parts[2])
				break
			}
		}
		if ownPath != "" {
			break
		}
	}
	if ownPath == "" {
		return memoryLimit{}, false
	}

	base := filepath.Join(cgroupRoot, "memory")
	limit := uint64(0)
	found := false
	for p := ownPath; ; p = filepath.Dir(p) {
		raw, err := os.ReadFile(filepath.Join(base, p, "memory.limit_in_bytes"))
		if err == nil {
			if v, ok := parseLimitValue(string(raw)); ok && v < cgroupV1Unlimited {
				if !found || v < limit {
					limit = v
					found = true
				}
			}
		}
		if p == "/" || p == "." {
			break
		}
	}
	if !found {
		return memoryLimit{}, false
	}

	var usage uint64
	if raw, err := os.ReadFile(filepath.Join(base, ownPath, "memory.usage_in_bytes")); err == nil {
		if v, ok := parseLimitValue(string(raw)); ok {
			usage = v
		}
	}
	return memoryLimit{LimitBytes: limit, UsageBytes: usage, Source: "cgroup-v1"}, true
}

// parseLimitValue parses a cgroup value file; "max" (v2 unlimited) is not a
// value.
func parseLimitValue(raw string) (uint64, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || s == "max" {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
