package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/process"
)

// With several models running, loaded_model must be deterministic — the
// largest weights win — not whatever Go map iteration happens to yield.
func TestServer_Hardware_LoadedModelPicksLargestDeterministically(t *testing.T) {
	dir := t.TempDir()
	small := filepath.Join(dir, "small.gguf")
	large := filepath.Join(dir, "large.gguf")
	if err := os.WriteFile(small, make([]byte, 1*1024*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(large, make([]byte, 3*1024*1024), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{Models: map[string]config.ModelConfig{
		"zz-small": {Cmd: "llama-server --model " + small},
		"aa-large": {Cmd: "llama-server --model " + large},
	}}
	local := newStubRouter([]string{"zz-small", "aa-large"}, "")
	local.running = map[string]process.ProcessState{
		"zz-small": process.StateReady,
		"aa-large": process.StateReady,
	}
	s := newTestServerWithConfig(cfg, local, newStubRouter(nil, ""))

	for i := 0; i < 25; i++ {
		id, mb := s.loadedModelInfo()
		if id != "aa-large" || mb != 3 {
			t.Fatalf("iteration %d: loadedModelInfo() = (%q, %d), want (aa-large, 3)", i, id, mb)
		}
	}
}

// A loaded model on a host without perf telemetry (and whose GGUF the fit
// engine cannot parse) must still serve /api/hardware with kv_estimate_mb 0 —
// the fit fallback is best-effort and must never break the endpoint.
func TestServer_Hardware_KVEstimateFallbackDegradesGracefully(t *testing.T) {
	dir := t.TempDir()
	ggufPath := filepath.Join(dir, "tiny.gguf")
	// Not a valid GGUF: forces fitForModel's parse-error path, so the fallback
	// sees a ModelFit with a nil KvMbAtMaxSafeCtx.
	if err := os.WriteFile(ggufPath, []byte("not a gguf"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{Models: map[string]config.ModelConfig{
		"tiny": {Cmd: "llama-server --model " + ggufPath},
	}}
	local := newStubRouter([]string{"tiny"}, "")
	local.running = map[string]process.ProcessState{"tiny": process.StateReady}
	s := newTestServerWithConfig(cfg, local, newStubRouter(nil, ""))

	w := getJSON(t, s, "/api/hardware")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	var resp struct {
		LoadedModel *struct {
			ID           string `json:"id"`
			ModelMB      int64  `json:"model_mb"`
			KVEstimateMB int64  `json:"kv_estimate_mb"`
		} `json:"loaded_model"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%q", err, w.Body.String())
	}
	if resp.LoadedModel == nil {
		t.Fatalf("expected loaded_model in response, body=%q", w.Body.String())
	}
	if resp.LoadedModel.ID != "tiny" {
		t.Errorf("loaded_model.id = %q, want %q", resp.LoadedModel.ID, "tiny")
	}
	if resp.LoadedModel.KVEstimateMB != 0 {
		t.Errorf("kv_estimate_mb = %d, want 0 when neither VRAM delta nor fit is computable", resp.LoadedModel.KVEstimateMB)
	}
}

// The inference block is the busy signal schedulers place work on: slots_total
// sums --parallel across running models (default 1), busy flips when in-flight
// requests cover every slot, and an idle host with no model running is NOT
// busy — it just needs a swap-in.
func TestServer_Hardware_InferenceLoadSignal(t *testing.T) {
	dir := t.TempDir()
	gguf := filepath.Join(dir, "m.gguf")
	if err := os.WriteFile(gguf, make([]byte, 1024*1024), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{Models: map[string]config.ModelConfig{
		"single": {Cmd: "llama-server --model " + gguf},
		"multi":  {Cmd: "llama-server --model " + gguf + " --parallel 4"},
	}}
	local := newStubRouter([]string{"single", "multi"}, "")
	local.running = map[string]process.ProcessState{
		"single": process.StateReady,
		"multi":  process.StateReady,
	}
	s := newTestServerWithConfig(cfg, local, newStubRouter(nil, ""))

	read := func() (inFlight, slots int, busy bool) {
		t.Helper()
		w := getJSON(t, s, "/api/hardware")
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
		}
		var resp struct {
			Inference *struct {
				InFlight   int  `json:"in_flight"`
				SlotsTotal int  `json:"slots_total"`
				Busy       bool `json:"busy"`
			} `json:"inference"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v body=%q", err, w.Body.String())
		}
		if resp.Inference == nil {
			t.Fatalf("expected inference in response, body=%q", w.Body.String())
		}
		return resp.Inference.InFlight, resp.Inference.SlotsTotal, resp.Inference.Busy
	}

	if inFlight, slots, busy := read(); inFlight != 0 || slots != 5 || busy {
		t.Errorf("idle: in_flight=%d slots_total=%d busy=%v, want 0, 5, false", inFlight, slots, busy)
	}

	// 4 in-flight of 5 slots: loaded but not saturated.
	for i := 0; i < 4; i++ {
		s.inflight.Increment()
	}
	if inFlight, slots, busy := read(); inFlight != 4 || slots != 5 || busy {
		t.Errorf("partial: in_flight=%d slots_total=%d busy=%v, want 4, 5, false", inFlight, slots, busy)
	}

	// 5 of 5: every slot occupied, a new request would queue.
	s.inflight.Increment()
	if inFlight, slots, busy := read(); inFlight != 5 || slots != 5 || !busy {
		t.Errorf("saturated: in_flight=%d slots_total=%d busy=%v, want 5, 5, true", inFlight, slots, busy)
	}

	// No model running: requests may be queued behind a swap-in, but the host
	// itself is placeable — busy must be false with slots_total 0.
	local.running = map[string]process.ProcessState{}
	if inFlight, slots, busy := read(); inFlight != 5 || slots != 0 || busy {
		t.Errorf("no models: in_flight=%d slots_total=%d busy=%v, want 5, 0, false", inFlight, slots, busy)
	}
}
