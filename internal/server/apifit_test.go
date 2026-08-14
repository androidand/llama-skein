package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/process"
	"github.com/androidand/llama-skein/pkg/apicontract"
)

func fitTestServer(t *testing.T, cfg config.Config) *Server {
	t.Helper()
	return newTestServerWithConfig(cfg, newStubRouter(nil, ""), newStubRouter(nil, ""))
}

// modelResident is the residency gate behind the whole-card VRAM budget: a
// model that is currently loaded must be budgeted against its total-card
// ceiling, not "free + its own weights" (which double-subtracts the resident
// KV and collapses max_fit_ctx to ~0 or a tiny fraction of what runs).
func TestModelResident(t *testing.T) {
	cases := []struct {
		name   string
		state  map[string]process.ProcessState
		model  string
		expect bool
	}{
		{
			name:   "ready model is resident",
			state:  map[string]process.ProcessState{"m": process.StateReady},
			model:  "m",
			expect: true,
		},
		{
			name:   "loading is not yet resident",
			state:  map[string]process.ProcessState{"m": process.StateStarting},
			model:  "m",
			expect: false,
		},
		{
			name:   "stopped model is not resident",
			state:  map[string]process.ProcessState{},
			model:  "m",
			expect: false,
		},
		{
			name:   "unknown model is not resident",
			state:  map[string]process.ProcessState{"other": process.StateReady},
			model:  "m",
			expect: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			local := newStubRouter(nil, "")
			local.running = c.state
			s := newTestServerWithConfig(config.Config{}, local, newStubRouter(nil, ""))
			if got := s.modelResident(c.model); got != c.expect {
				t.Errorf("modelResident(%q) = %v, want %v", c.model, got, c.expect)
			}
		})
	}
}

// TestServer_Fit_GGUFCacheByMtime is the GGUF cache-by-mtime regression; see
// parseGGUFCached. Fit output itself is covered by TestModelGetsWholeGPU and
// the budget-behavior cases above.

func getJSON(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// TestModelGetsWholeGPU is a regression for the opencode/skein ctx-size
// oscillation: on a host running several models in one exclusive swap group
// (z4: three models sharing one 48GB GPU), fit was budgeted against
// momentary free VRAM, which depends entirely on whether some OTHER model
// happened to be loaded at query time — even though an exclusive group
// guarantees each model gets the whole card once it's its turn. skein's
// context-fit sweep reacts to that live number every cycle, so the model's
// --ctx-size kept getting rewritten up and down for no real reason.
func TestModelGetsWholeGPU(t *testing.T) {
	cases := []struct {
		name   string
		cfg    config.Config
		model  string
		expect bool
	}{
		{
			name: "solo model in an exclusive group gets the whole card",
			cfg: config.Config{Groups: map[string]config.GroupConfig{
				"default": {Exclusive: true, Members: []string{"a", "b", "c"}},
			}},
			model:  "b",
			expect: true,
		},
		{
			name: "non-exclusive group shares the card, must use live free VRAM",
			cfg: config.Config{Groups: map[string]config.GroupConfig{
				"default": {Exclusive: false, Members: []string{"a", "b"}},
			}},
			model:  "a",
			expect: false,
		},
		{
			name: "a persistent group elsewhere survives eviction, card is still shared",
			cfg: config.Config{Groups: map[string]config.GroupConfig{
				"default":   {Exclusive: true, Members: []string{"a", "b"}},
				"always-on": {Persistent: true, Members: []string{"c"}},
			}},
			model:  "a",
			expect: false,
		},
		{
			name:   "model not in any group",
			cfg:    config.Config{Groups: map[string]config.GroupConfig{"default": {Exclusive: true, Members: []string{"a"}}}},
			model:  "z",
			expect: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := modelGetsWholeGPU(c.cfg, c.model); got != c.expect {
				t.Errorf("modelGetsWholeGPU = %v, want %v", got, c.expect)
			}
		})
	}
}

func TestServer_Fit_UnknownModel404(t *testing.T) {
	s := fitTestServer(t, config.Config{Models: map[string]config.ModelConfig{}})
	if w := getJSON(t, s, "/api/fit/nope"); w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown model, got %d", w.Code)
	}
}

// An MLX model we cannot locate reports a clear reason rather than a wrong number —
// and reports it as "unknown", not as a negative fit verdict.
func TestServer_Fit_MLXReportsUnsupported(t *testing.T) {
	cfg := config.Config{Models: map[string]config.ModelConfig{
		"mlx1": {Cmd: "mlx_lm.server --model mlx-community/x", Backend: config.BackendMLX},
	}}
	s := fitTestServer(t, cfg)
	w := getJSON(t, s, "/api/fit/mlx1")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	var mf apicontract.ModelFit
	json.Unmarshal(w.Body.Bytes(), &mf)
	// "unknown", not "no": nothing was measured (no useModelName means we cannot find
	// the weights). "no" is a verdict meaning the model will not fit this host, and
	// consumers surface it that way.
	if mf.FitLevel != apicontract.Unknown || mf.Reason == nil {
		t.Errorf("expected fit=unknown with a reason for mlx, got level=%q reason=%v", mf.FitLevel, mf.Reason)
	}
}

// A backend whose weights are not modeled at all must also report "unknown" rather
// than a negative verdict — every backend: vllm model was previously reported as "no".
func TestServer_Fit_UnmodeledBackendReportsUnknown(t *testing.T) {
	cfg := config.Config{Models: map[string]config.ModelConfig{
		"vllm1": {Cmd: "vllm serve some/model --port 1234", Backend: config.BackendVLLM},
	}}
	s := fitTestServer(t, cfg)
	w := getJSON(t, s, "/api/fit/vllm1")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
	var mf apicontract.ModelFit
	json.Unmarshal(w.Body.Bytes(), &mf)
	if mf.FitLevel != apicontract.Unknown {
		t.Errorf("expected fit=unknown for an unmodeled backend, got %q", mf.FitLevel)
	}
	if mf.Reason == nil {
		t.Error("expected a reason explaining why fit was not computed")
	}
}

// The report lists every configured model and is registered at /api/fit.
func TestServer_Fit_ReportListsModels(t *testing.T) {
	cfg := config.Config{Models: map[string]config.ModelConfig{
		"a": {Cmd: "mlx_lm.server --model x", Backend: config.BackendMLX},
		"b": {Cmd: "llama-server --model /missing.gguf"},
	}}
	s := fitTestServer(t, cfg)
	w := getJSON(t, s, "/api/fit")
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var report apicontract.FitReport
	json.Unmarshal(w.Body.Bytes(), &report)
	if len(report.Models) != 2 {
		t.Errorf("expected 2 models in report, got %d", len(report.Models))
	}
}

// minimalGGUF returns a syntactically valid GGUF header with no tensors and
// no KV entries, padded to size bytes so FileSize distinguishes rewrites.
func minimalGGUF(size int) []byte {
	b := make([]byte, 0, size)
	b = append(b, 'G', 'G', 'U', 'F')
	b = append(b, 3, 0, 0, 0)          // version 3, little-endian
	b = append(b, make([]byte, 16)...) // tensor count 0, kv count 0
	for len(b) < size {
		b = append(b, 0)
	}
	return b
}

// The GGUF cache serves by (path, mtime): same mtime returns the cached
// parse even if content changed; a newer mtime re-parses.
func TestServer_Fit_GGUFCacheByMtime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.gguf")
	if err := os.WriteFile(path, minimalGGUF(256), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	origMtime := info.ModTime()

	s := fitTestServer(t, config.Config{Models: map[string]config.ModelConfig{}})

	g1, err := s.parseGGUFCached(path)
	if err != nil {
		t.Fatalf("first parse: %v", err)
	}
	if g1.FileSize != 256 {
		t.Fatalf("FileSize = %d, want 256", g1.FileSize)
	}

	// Rewrite with different content but restore the original mtime.
	if err := os.WriteFile(path, minimalGGUF(512), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, origMtime, origMtime); err != nil {
		t.Fatal(err)
	}
	g2, err := s.parseGGUFCached(path)
	if err != nil {
		t.Fatalf("cached parse: %v", err)
	}
	if g2.FileSize != 256 {
		t.Fatalf("expected stale cached parse (FileSize 256), got %d", g2.FileSize)
	}

	// Bump mtime: must re-parse and see the new content.
	newer := origMtime.Add(2 * time.Second)
	if err := os.Chtimes(path, newer, newer); err != nil {
		t.Fatal(err)
	}
	g3, err := s.parseGGUFCached(path)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if g3.FileSize != 512 {
		t.Fatalf("expected fresh parse (FileSize 512), got %d", g3.FileSize)
	}
}
