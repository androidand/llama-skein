package router

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/androidand/llama-skein/internal/config"
)

func jsonRequest(body string) *http.Request {
	r, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestExtractContext_ModelMissingSentinel(t *testing.T) {
	tests := []struct {
		name        string
		req         *http.Request
		wantMissing bool
	}{
		{"json model key missing", jsonRequest(`{"stream":true}`), true},
		{"json model empty", jsonRequest(`{"model":""}`), true},
		{"json model present", jsonRequest(`{"model":"m1"}`), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ExtractContext(tt.req)
			if got := errors.Is(err, ErrModelMissing); got != tt.wantMissing {
				t.Errorf("errors.Is(err, ErrModelMissing)=%v want %v (err=%v)", got, tt.wantMissing, err)
			}
		})
	}
}

func TestFetchContext_DefaultModel(t *testing.T) {
	cfg := config.Config{
		Models:       map[string]config.ModelConfig{"m1": {}, "m2": {}},
		DefaultModel: "m1",
	}

	t.Run("missing model falls back to default", func(t *testing.T) {
		data, err := FetchContext(jsonRequest(`{"stream":true}`), cfg)
		if err != nil {
			t.Fatalf("FetchContext: %v", err)
		}
		if data.Model != "m1" || data.ModelID != "m1" {
			t.Errorf("Model=%q ModelID=%q want m1/m1", data.Model, data.ModelID)
		}
		if !data.ModelDefaulted {
			t.Error("ModelDefaulted=false want true")
		}
		if !data.Streaming {
			t.Error("Streaming=false want true (parsed from body)")
		}
	})

	t.Run("explicit model wins over default", func(t *testing.T) {
		data, err := FetchContext(jsonRequest(`{"model":"m2"}`), cfg)
		if err != nil {
			t.Fatalf("FetchContext: %v", err)
		}
		if data.Model != "m2" || data.ModelDefaulted {
			t.Errorf("Model=%q ModelDefaulted=%v want m2/false", data.Model, data.ModelDefaulted)
		}
	})

	t.Run("GET query falls back to default", func(t *testing.T) {
		r, _ := http.NewRequest(http.MethodGet, "/v1/audio/voices", nil)
		data, err := FetchContext(r, cfg)
		if err != nil {
			t.Fatalf("FetchContext: %v", err)
		}
		if data.Model != "m1" || !data.ModelDefaulted {
			t.Errorf("Model=%q ModelDefaulted=%v want m1/true", data.Model, data.ModelDefaulted)
		}
	})

	t.Run("no default configured errors", func(t *testing.T) {
		noDefault := config.Config{Models: map[string]config.ModelConfig{"m1": {}}}
		if _, err := FetchContext(jsonRequest(`{}`), noDefault); !errors.Is(err, ErrNoModelInContext) {
			t.Errorf("err=%v want ErrNoModelInContext", err)
		}
	})
}
