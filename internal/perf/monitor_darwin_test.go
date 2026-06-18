package perf

import "testing"

// Real vm_stat output from an idle Apple Silicon Mac (36 GB, 16 KB pages).
// free+inactive+purgeable should read as a large reclaimable pool — the value
// gopsutil's Available undercounts to near zero, which made the memory guard
// misfire. free=17034MB + inactive=5811MB + purgeable=904MB ≈ 23749 MB.
const sampleVmStat = `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                              1090176.
Pages active:                             461120.
Pages inactive:                           371904.
Pages speculative:                          2048.
Pages throttled:                               0.
Pages wired down:                         164416.
Pages purgeable:                           57856.
"Translation faults":                  123456789.
Pages stored in compressor:               300000.
Pages occupied by compressor:             140928.
`

func TestParseVmStatAvailableMB(t *testing.T) {
	got := parseVmStatAvailableMB(sampleVmStat)
	// (1090176 + 371904 + 57856) pages * 16384 B / 1MiB = 23749 MB
	const want = 23749
	if got != want {
		t.Fatalf("parseVmStatAvailableMB = %d MB, want %d MB", got, want)
	}
}

func TestParseVmStatAvailableMB_Garbage(t *testing.T) {
	if got := parseVmStatAvailableMB("not vm_stat output\n"); got != 0 {
		t.Fatalf("expected 0 for unparseable input, got %d", got)
	}
}
