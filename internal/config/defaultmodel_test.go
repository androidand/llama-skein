package config

import (
	"strings"
	"testing"
)

func TestConfig_DefaultModel(t *testing.T) {
	cfg, err := LoadConfigFromReader(strings.NewReader(`
defaultModel: m1
models:
  m1:
    cmd: server --port ${PORT}
`))
	if err != nil {
		t.Fatalf("LoadConfigFromReader: %v", err)
	}
	if cfg.DefaultModel != "m1" {
		t.Errorf("DefaultModel=%q want %q", cfg.DefaultModel, "m1")
	}
}

func TestConfig_DefaultModel_Alias(t *testing.T) {
	cfg, err := LoadConfigFromReader(strings.NewReader(`
defaultModel: shorty
models:
  m1:
    cmd: server --port ${PORT}
    aliases:
      - shorty
`))
	if err != nil {
		t.Fatalf("LoadConfigFromReader: %v", err)
	}
	if real, found := cfg.RealModelName(cfg.DefaultModel); !found || real != "m1" {
		t.Errorf("RealModelName(%q)=(%q,%v) want (m1,true)", cfg.DefaultModel, real, found)
	}
}

func TestConfig_DefaultModel_UnknownRejected(t *testing.T) {
	_, err := LoadConfigFromReader(strings.NewReader(`
defaultModel: no-such-model
models:
  m1:
    cmd: server --port ${PORT}
`))
	if err == nil || !strings.Contains(err.Error(), "defaultModel") {
		t.Fatalf("want defaultModel validation error, got %v", err)
	}
}
