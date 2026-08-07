package config

import "testing"

func TestPlacementConfig_Defaults(t *testing.T) {
	var p PlacementConfig
	if p.EffectiveMode() != PlacementModeAuto {
		t.Fatalf("default mode = %q", p.EffectiveMode())
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("zero value must validate: %v", err)
	}
	// 128 GiB host: 10% (~12.8 GiB) beats the 12 GiB floor.
	if got := p.HostReserveMB(128 * 1024); got != 128*1024/10 {
		t.Fatalf("host reserve for 128GiB = %d", got)
	}
	// 48 GiB host: the 12 GiB floor wins.
	if got := p.HostReserveMB(48 * 1024); got != 12*1024 {
		t.Fatalf("host reserve for 48GiB = %d", got)
	}
	// 48 GB card: 5% (2.4 GiB) beats the 2 GiB floor.
	if got := p.GpuReserveMB(48 * 1024); got != 48*1024/20 {
		t.Fatalf("gpu reserve for 48GiB = %d", got)
	}
	// 16 GB card: the 2 GiB floor wins.
	if got := p.GpuReserveMB(16 * 1024); got != 2*1024 {
		t.Fatalf("gpu reserve for 16GiB = %d", got)
	}
	if p.MinCtx() != defaultPlacementMinCtx {
		t.Fatalf("min ctx = %d", p.MinCtx())
	}
}

func TestPlacementConfig_Explicit(t *testing.T) {
	p := PlacementConfig{Mode: "hybrid", HostReserveGiB: 16, GpuReserveGiB: 3, MinimumContext: 32768}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.HostReserveMB(128*1024) != 16*1024 || p.GpuReserveMB(48*1024) != 3*1024 || p.MinCtx() != 32768 {
		t.Fatal("explicit values must win over defaults")
	}
}

func TestPlacementConfig_InvalidMode(t *testing.T) {
	p := PlacementConfig{Mode: "turbo"}
	if err := p.Validate(); err == nil {
		t.Fatal("unknown mode must fail validation")
	}
}
