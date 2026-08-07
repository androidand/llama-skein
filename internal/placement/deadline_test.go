package placement

import "testing"

// Regression (z4, 2026-08-07): a 91 GB model was repeatedly killed at the
// 120 s default health check mid-load and reported as a broken model. The
// deadline has to scale with the model.
func TestLoadDeadlineSeconds(t *testing.T) {
	const gb = int64(1) << 30

	// Unknown size: no opinion, caller keeps its configured value.
	if got := LoadDeadlineSeconds(0, ModeGPU); got != 0 {
		t.Fatalf("unknown size = %d, want 0 (no opinion)", got)
	}

	// A small model stays near the base deadline.
	small := LoadDeadlineSeconds(9*gb, ModeGPU)
	if small < loadBaseSeconds || small > loadBaseSeconds+200 {
		t.Fatalf("9 GB deadline = %ds, want just above the %ds base", small, loadBaseSeconds)
	}

	// The 91 GB acceptance model must get far more than the 120 s that
	// killed it.
	big := LoadDeadlineSeconds(91*gb, ModeHybrid)
	if big <= 120 {
		t.Fatalf("91 GB hybrid deadline = %ds — the exact value that failed", big)
	}
	if big > loadMaxSeconds {
		t.Fatalf("deadline = %ds exceeds the cap %ds", big, loadMaxSeconds)
	}

	// Bigger models get longer, and a hybrid placement gets longer than the
	// same model would full-GPU (CPU-side expert setup on top of the read).
	if LoadDeadlineSeconds(40*gb, ModeGPU) <= LoadDeadlineSeconds(9*gb, ModeGPU) {
		t.Fatal("deadline must grow with model size")
	}
	if LoadDeadlineSeconds(40*gb, ModeHybrid) <= LoadDeadlineSeconds(40*gb, ModeGPU) {
		t.Fatal("a hybrid placement must get at least as long as full-GPU")
	}

	// Absurdly large models are capped rather than waiting forever.
	if got := LoadDeadlineSeconds(2000*gb, ModeHybrid); got != loadMaxSeconds {
		t.Fatalf("cap = %d, want %d", got, loadMaxSeconds)
	}
}
