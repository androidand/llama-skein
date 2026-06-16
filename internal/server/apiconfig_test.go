package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/pkg/apicontract"
)

func TestServer_ConfigGetModelDetail(t *testing.T) {
	cmd := "llama-server --model /m.gguf --ctx-size 4096 --n-cpu-moe 8 --cache-type-k q8_0"
	cfg := config.Config{Models: map[string]config.ModelConfig{
		"m1": {Cmd: cmd, ConcurrencyLimit: 2, UnloadAfter: 300},
	}}
	s := newTestServerWithConfig(cfg, newStubRouter(nil, ""), newStubRouter(nil, ""))

	req := httptest.NewRequest(http.MethodGet, "/api/config/models/m1", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}

	// The response must decode into the generated detail type.
	var d apicontract.ConfigModelDetail
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode ConfigModelDetail: %v body=%q", err, w.Body.String())
	}
	if d.Id != "m1" || d.Cmd != cmd {
		t.Errorf("id/cmd = %q/%q", d.Id, d.Cmd)
	}
	// ctx_size stays a raw string (consumers like skein decode it as string).
	if d.CtxSize == nil || *d.CtxSize != "4096" {
		t.Errorf("ctx_size = %v, want \"4096\"", d.CtxSize)
	}
	if d.ConcurrencyLimit == nil || *d.ConcurrencyLimit != 2 {
		t.Errorf("concurrencyLimit = %v, want 2", d.ConcurrencyLimit)
	}
	if d.Ttl == nil || *d.Ttl != 300 {
		t.Errorf("ttl = %v, want 300", d.Ttl)
	}
	// Offload read-back.
	if d.NCpuMoe == nil || *d.NCpuMoe != 8 {
		t.Errorf("n_cpu_moe = %v, want 8", d.NCpuMoe)
	}
	if d.Flags == nil || (*d.Flags)["--cache-type-k"] != "q8_0" {
		t.Errorf("flags missing --cache-type-k: %v", d.Flags)
	}
}

func TestServer_ConfigInfoTyped(t *testing.T) {
	cfg := config.Config{
		Models:       map[string]config.ModelConfig{"m1": {Cmd: "srv --model /a"}, "m2": {Cmd: "srv --model /b"}},
		DefaultModel: "m1",
	}
	s := newTestServerWithConfig(cfg, newStubRouter(nil, ""), newStubRouter(nil, ""))

	req := httptest.NewRequest(http.MethodGet, "/api/config/info", nil)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var info apicontract.ConfigInfoResponse
	if err := json.Unmarshal(w.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode ConfigInfoResponse: %v body=%q", err, w.Body.String())
	}
	if info.ModelCount != 2 || len(info.Models) != 2 {
		t.Errorf("model_count=%d models=%d, want 2/2", info.ModelCount, len(info.Models))
	}
	if info.DefaultModel == nil || *info.DefaultModel != "m1" {
		t.Errorf("default_model = %v, want m1", info.DefaultModel)
	}
}
