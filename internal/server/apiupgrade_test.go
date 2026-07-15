package server

import (
	"strings"
	"testing"
)

func TestSourceCmakeArgs(t *testing.T) {
	trueVal, falseVal := true, false

	cases := []struct {
		name         string
		rocmRoot     string
		gfx          string
		rocwmmaFattn *bool
		wantContains []string
		wantAbsent   []string
	}{
		{
			name:       "no ROCm — generic build, gfx/rocwmma ignored",
			rocmRoot:   "",
			gfx:        "gfx1100",
			wantAbsent: []string{"AMDGPU_TARGETS", "GGML_HIP", "ROCWMMA_FATTN"},
		},
		{
			name:         "ROCm detected, gfx tailored, rocwmmaFattn off",
			rocmRoot:     "/opt/rocm",
			gfx:          "gfx1100",
			rocwmmaFattn: &falseVal,
			wantContains: []string{"-DAMDGPU_TARGETS=gfx1100", "-DGGML_HIP=ON", "-DGGML_HIP_ROCWMMA_FATTN=OFF", "-DCMAKE_HIP_COMPILER=/opt/rocm/bin/amdclang++"},
		},
		{
			name:         "ROCm detected, rocwmmaFattn explicitly on",
			rocmRoot:     "/opt/rocm",
			gfx:          "gfx1100",
			rocwmmaFattn: &trueVal,
			wantContains: []string{"-DGGML_HIP_ROCWMMA_FATTN=ON"},
		},
		{
			name:       "ROCm detected, rocwmmaFattn unset — no override emitted",
			rocmRoot:   "/opt/rocm",
			gfx:        "gfx1100",
			wantAbsent: []string{"ROCWMMA_FATTN"},
		},
		{
			name:       "ROCm detected, gfx unknown — no AMDGPU_TARGETS pin",
			rocmRoot:   "/opt/rocm",
			gfx:        "",
			wantAbsent: []string{"AMDGPU_TARGETS"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args, _ := sourceCmakeArgs("/tmp/build", "/tmp/workspace", c.rocmRoot, c.gfx, c.rocwmmaFattn)
			joined := strings.Join(args, " ")
			for _, want := range c.wantContains {
				if !strings.Contains(joined, want) {
					t.Errorf("args %v missing %q", args, want)
				}
			}
			for _, absent := range c.wantAbsent {
				if strings.Contains(joined, absent) {
					t.Errorf("args %v should not contain %q", args, absent)
				}
			}
			if args[len(args)-1] != "/tmp/workspace" {
				t.Errorf("workspace source dir must be the final cmake arg, got %v", args)
			}
		})
	}
}
