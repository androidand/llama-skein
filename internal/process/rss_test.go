package process

import "testing"

// VmRSS counts resident file-backed pages, which is exactly what mmap'd
// model weights are — the reason this is measured per-process rather than
// from host "available memory", which barely moves for a hybrid placement.
func TestParseVmRSS(t *testing.T) {
	status := `Name:	llama-server
State:	S (sleeping)
VmPeak:	 96000000 kB
VmSize:	 95000000 kB
VmRSS:	 51380224 kB
RssAnon:	  2097152 kB
RssFile:	 49283072 kB
`
	got := parseVmRSS(status)
	want := int64(51380224) * 1024
	if got != want {
		t.Fatalf("parseVmRSS = %d, want %d", got, want)
	}
}

func TestParseVmRSS_Missing(t *testing.T) {
	if got := parseVmRSS("Name:\tx\nState:\tS\n"); got != 0 {
		t.Fatalf("absent VmRSS must read as 0, got %d", got)
	}
	if got := parseVmRSS(""); got != 0 {
		t.Fatalf("empty status must read as 0, got %d", got)
	}
	if got := parseVmRSS("VmRSS:\tnotanumber kB\n"); got != 0 {
		t.Fatalf("unparseable VmRSS must read as 0, got %d", got)
	}
}

// A process that is not running has no PID and reports nothing, rather than
// reading some other process's memory.
func TestResidentBytes_NotRunning(t *testing.T) {
	p := &ProcessCommand{}
	if got := p.ResidentBytes(); got != 0 {
		t.Fatalf("a stopped process must report 0, got %d", got)
	}
}
