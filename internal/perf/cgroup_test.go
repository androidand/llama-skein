package perf

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCgroup_V2Limited(t *testing.T) {
	dir := t.TempDir()
	proc := filepath.Join(dir, "cgroup")
	root := filepath.Join(dir, "sysfs")
	writeFile(t, proc, "0::/system.slice/llama.service\n")
	writeFile(t, filepath.Join(root, "system.slice/llama.service/memory.max"), "51539607552\n") // 48 GiB
	writeFile(t, filepath.Join(root, "system.slice/llama.service/memory.current"), "10737418240\n")

	lim, ok := effectiveMemoryLimitFrom(proc, root)
	if !ok {
		t.Fatal("expected a limit")
	}
	if lim.Source != "cgroup-v2" {
		t.Fatalf("source = %q", lim.Source)
	}
	if lim.LimitBytes != 51539607552 {
		t.Fatalf("limit = %d", lim.LimitBytes)
	}
	if lim.UsageBytes != 10737418240 {
		t.Fatalf("usage = %d", lim.UsageBytes)
	}
}

func TestCgroup_V2AncestorTighter(t *testing.T) {
	dir := t.TempDir()
	proc := filepath.Join(dir, "cgroup")
	root := filepath.Join(dir, "sysfs")
	writeFile(t, proc, "0::/parent/child\n")
	writeFile(t, filepath.Join(root, "parent/child/memory.max"), "max\n")
	writeFile(t, filepath.Join(root, "parent/memory.max"), "1073741824\n") // 1 GiB

	lim, ok := effectiveMemoryLimitFrom(proc, root)
	if !ok {
		t.Fatal("expected a limit from the ancestor")
	}
	if lim.LimitBytes != 1073741824 {
		t.Fatalf("limit = %d", lim.LimitBytes)
	}
}

func TestCgroup_V2Unlimited(t *testing.T) {
	dir := t.TempDir()
	proc := filepath.Join(dir, "cgroup")
	root := filepath.Join(dir, "sysfs")
	writeFile(t, proc, "0::/\n")
	writeFile(t, filepath.Join(root, "memory.max"), "max\n")

	if _, ok := effectiveMemoryLimitFrom(proc, root); ok {
		t.Fatal("unlimited cgroup must report no limit")
	}
}

func TestCgroup_V1Fallback(t *testing.T) {
	dir := t.TempDir()
	proc := filepath.Join(dir, "cgroup")
	root := filepath.Join(dir, "sysfs")
	writeFile(t, proc, "11:memory:/lxc/102\n10:cpu,cpuacct:/lxc/102\n")
	writeFile(t, filepath.Join(root, "memory/lxc/102/memory.limit_in_bytes"), "51539607552\n")
	writeFile(t, filepath.Join(root, "memory/lxc/102/memory.usage_in_bytes"), "1073741824\n")

	lim, ok := effectiveMemoryLimitFrom(proc, root)
	if !ok {
		t.Fatal("expected a v1 limit")
	}
	if lim.Source != "cgroup-v1" {
		t.Fatalf("source = %q", lim.Source)
	}
	if lim.LimitBytes != 51539607552 {
		t.Fatalf("limit = %d", lim.LimitBytes)
	}
}

func TestCgroup_V1Unlimited(t *testing.T) {
	dir := t.TempDir()
	proc := filepath.Join(dir, "cgroup")
	root := filepath.Join(dir, "sysfs")
	writeFile(t, proc, "11:memory:/\n")
	// v1 encodes "unlimited" as a huge number, not "max".
	writeFile(t, filepath.Join(root, "memory/memory.limit_in_bytes"), "9223372036854771712\n")

	if _, ok := effectiveMemoryLimitFrom(proc, root); ok {
		t.Fatal("v1 unlimited must report no limit")
	}
}

func TestCgroup_NoCgroupFile(t *testing.T) {
	dir := t.TempDir()
	if _, ok := effectiveMemoryLimitFrom(filepath.Join(dir, "missing"), dir); ok {
		t.Fatal("missing /proc/self/cgroup must report no limit")
	}
}

func TestApplyEffectiveMemoryLimit_Clamped(t *testing.T) {
	s := SysStat{MemTotalMB: 128000, MemAvailableMB: 100000}
	got := applyEffectiveMemoryLimit(s, func() (memoryLimit, bool) {
		return memoryLimit{LimitBytes: 48 << 30, UsageBytes: 8 << 30, Source: "cgroup-v2"}, true
	})
	if got.MemLimitSource != "cgroup-v2" {
		t.Fatalf("source = %q", got.MemLimitSource)
	}
	if got.MemEffectiveTotalMB != 48*1024 {
		t.Fatalf("effective total = %d", got.MemEffectiveTotalMB)
	}
	if want := (48 - 8) * 1024; got.MemEffectiveAvailableMB != want {
		t.Fatalf("effective available = %d, want %d", got.MemEffectiveAvailableMB, want)
	}
	// Raw figures untouched.
	if got.MemTotalMB != 128000 || got.MemAvailableMB != 100000 {
		t.Fatal("raw figures must not change")
	}
}

func TestApplyEffectiveMemoryLimit_NoLimit(t *testing.T) {
	s := SysStat{MemTotalMB: 64000, MemAvailableMB: 30000}
	got := applyEffectiveMemoryLimit(s, func() (memoryLimit, bool) {
		return memoryLimit{}, false
	})
	if got.MemLimitSource != "none" {
		t.Fatalf("source = %q", got.MemLimitSource)
	}
	if got.MemEffectiveTotalMB != 64000 || got.MemEffectiveAvailableMB != 30000 {
		t.Fatal("effective must equal raw without a limit")
	}
}

func TestApplyEffectiveMemoryLimit_LimitAbovePhysical(t *testing.T) {
	s := SysStat{MemTotalMB: 64000, MemAvailableMB: 30000}
	got := applyEffectiveMemoryLimit(s, func() (memoryLimit, bool) {
		return memoryLimit{LimitBytes: 512 << 30, Source: "cgroup-v2"}, true
	})
	if got.MemLimitSource != "none" {
		t.Fatalf("a limit above physical RAM constrains nothing; source = %q", got.MemLimitSource)
	}
	if got.MemEffectiveTotalMB != 64000 {
		t.Fatalf("effective total = %d", got.MemEffectiveTotalMB)
	}
}

func TestApplyEffectiveMemoryLimit_LxcfsAlreadyVirtualized(t *testing.T) {
	// Proxmox LXC: lxcfs already shows the limit in /proc/meminfo, and the
	// cgroup limit equals MemTotal. Effective figures match raw; the source
	// still names the cgroup so operators can see a limit applies.
	s := SysStat{MemTotalMB: 48 * 1024, MemAvailableMB: 38 * 1024}
	got := applyEffectiveMemoryLimit(s, func() (memoryLimit, bool) {
		return memoryLimit{LimitBytes: 48 << 30, UsageBytes: 9 << 30, Source: "cgroup-v2"}, true
	})
	if got.MemLimitSource != "cgroup-v2" {
		t.Fatalf("source = %q", got.MemLimitSource)
	}
	if got.MemEffectiveTotalMB != 48*1024 {
		t.Fatalf("effective total = %d", got.MemEffectiveTotalMB)
	}
	if got.MemEffectiveAvailableMB != 38*1024 {
		t.Fatalf("effective available = %d (must keep the tighter of the two)", got.MemEffectiveAvailableMB)
	}
}
