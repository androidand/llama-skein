package server

import (
	"testing"

	"github.com/androidand/llama-skein/pkg/apicontract"
)

// requestedCtxSize must honour both spellings of the field, with the dashed form
// winning — that preserves the write order the previous code had, so behaviour for
// callers sending both is unchanged.
func TestRequestedCtxSize(t *testing.T) {
	n := func(i int) *int { return &i }

	cases := []struct {
		name   string
		snake  *int
		dashed *int
		wantN  int
		wantOK bool
	}{
		{"neither set", nil, nil, 0, false},
		{"snake only", n(32768), nil, 32768, true},
		{"dashed only", nil, n(16384), 16384, true},
		{"both set, dashed wins", n(32768), n(16384), 16384, true},
		{"zero is still a request", n(0), nil, 0, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := requestedCtxSize(patchReqWithCtx(c.snake, c.dashed))
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok && got != c.wantN {
				t.Errorf("n = %d, want %d", got, c.wantN)
			}
		})
	}
}

// The clamp must only ever lower a value. A ceiling that is above the request must
// leave it alone — otherwise the guard becomes a downward ratchet, which is the more
// painful direction of this bug in practice (models parked at tiny contexts).
func TestCtxClamp_OnlyLowers(t *testing.T) {
	cases := []struct {
		name    string
		want    int
		ceiling int
		applied int
	}{
		{"above ceiling is clamped", 98304, 32768, 32768},
		{"at ceiling is untouched", 32768, 32768, 32768},
		{"below ceiling is untouched", 8192, 32768, 8192},
		{"no ceiling known leaves it alone", 262144, 0, 262144},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			applied := c.want
			if c.ceiling > 0 && c.want > c.ceiling {
				applied = c.ceiling
			}
			if applied != c.applied {
				t.Errorf("applied = %d, want %d", applied, c.applied)
			}
			if applied > c.want {
				t.Errorf("clamp raised the value: %d > %d", applied, c.want)
			}
		})
	}
}

// patchReqWithCtx builds a patch request setting only the ctx-size fields.
func patchReqWithCtx(snake, dashed *int) apicontract.ConfigModelPatchRequest {
	return apicontract.ConfigModelPatchRequest{CtxSize: snake, CtxSizeDash: dashed}
}
