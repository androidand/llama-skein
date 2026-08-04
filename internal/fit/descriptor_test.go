package fit

import (
	"testing"

	"github.com/androidand/llama-skein/pkg/gguf"
)

// A hypothetical model scored with the SAME dims as a real GGUF must produce
// an identical shape (and therefore an identical fit result) to scoring that
// GGUF directly — the descriptor path is a second front door onto the same
// engine, not a separate approximation, when dims are supplied explicitly.
func TestShapeFromDescriptor_ExplicitDimsMatchGGUF(t *testing.T) {
	g := &gguf.GGUF{
		LayerCount: 32, HeadCount: 32, HeadCountKV: 8,
		EmbeddingLength: 4096, KeyLength: 128, ValueLength: 128,
		ContextLength: 32768,
		FileSize:      9 * 1024 * 1024 * 1024,
	}
	ggufShape := ShapeFromGGUF(g)

	d := Descriptor{
		LayerCount: 32, HeadCount: 32, HeadCountKV: 8,
		EmbeddingLength: 4096, HeadDim: 128, TrainedCtx: 32768,
	}
	descShape, estimated := ShapeFromDescriptor(d, g.FileSize)
	if estimated {
		t.Fatal("all dims supplied explicitly; must not be flagged estimated")
	}
	if descShape != ggufShape {
		t.Fatalf("descriptor shape %+v != gguf shape %+v", descShape, ggufShape)
	}

	p := Params{VRAMTotalMB: 24000, ConfiguredCtx: 16384}
	got := AnalyzeShape(descShape, p)
	want := AnalyzeShape(ggufShape, p)
	if got != want {
		t.Fatalf("descriptor-path result %+v != gguf-path result %+v", got, want)
	}
}

// A catalog entry with no known dims (the common case: browsing HuggingFace
// before download) must still score to something usable — estimated=true and
// sane positive dims across the size range the dense preset table covers.
func TestShapeFromDescriptor_EstimatesAcrossSizeRange(t *testing.T) {
	for _, paramsB := range []float64{0.5, 1.5, 3, 7, 14, 30, 70, 150} {
		d := Descriptor{ParamsB: paramsB}
		shape, estimated := ShapeFromDescriptor(d, int64(paramsB*1e9*0.55)) // rough Q4 bytes/param
		if !estimated {
			t.Errorf("paramsB=%v: expected estimated=true with no explicit dims", paramsB)
		}
		if shape.LayerCount <= 0 || shape.EmbeddingLength <= 0 || shape.HeadCount <= 0 || shape.HeadCountKV <= 0 {
			t.Fatalf("paramsB=%v: non-positive core dim in estimated shape %+v", paramsB, shape)
		}
		if shape.HeadCountKV > shape.HeadCount {
			t.Errorf("paramsB=%v: kv heads %d > heads %d", paramsB, shape.HeadCountKV, shape.HeadCount)
		}
		if shape.TrainedCtx <= 0 {
			t.Errorf("paramsB=%v: non-positive trained ctx", paramsB)
		}

		res := AnalyzeShape(shape, Params{VRAMTotalMB: 24000})
		if res.FitLevel == "" {
			t.Errorf("paramsB=%v: empty fit level, engine could not score the estimated shape", paramsB)
		}
	}
}

// Explicit dims without an explicit KV-head count must assume GQA (a
// conservative near-universal default), not fall back to full MHA — MHA
// would overcount KV several-fold and reject models that actually fit.
func TestShapeFromDescriptor_MissingKVHeadsAssumesGQA(t *testing.T) {
	d := Descriptor{LayerCount: 32, EmbeddingLength: 4096, HeadCount: 32, HeadDim: 128, TrainedCtx: 32768}
	shape, estimated := ShapeFromDescriptor(d, 9*1024*1024*1024)
	if !estimated {
		t.Fatal("missing head_count_kv must be flagged estimated")
	}
	if shape.HeadCountKV != 8 {
		t.Errorf("HeadCountKV = %d, want 8 (assumed GQA default)", shape.HeadCountKV)
	}
}

// Params.Unproven is the whole reason this file exists: a gallery candidate
// has never loaded, so it must not get the "deployed model" rescue from "no"
// to "marginal", and must not get flagged UnderConfigured. Both safety nets
// exist for models an operator has PROVEN load at their configured size.
func TestParams_Unproven_NoRescueFromNo(t *testing.T) {
	shape := ModelShape{LayerCount: 32, EmbeddingLength: 4096, HeadCount: 32, HeadCountKV: 8, WeightBytes: 9 * 1024 * 1024 * 1024, TrainedCtx: 32768}

	proven := AnalyzeShape(shape, Params{VRAMTotalMB: 1, ConfiguredCtx: 4096})
	if proven.FitLevel != "marginal" {
		t.Fatalf("proven configured model: want rescue to marginal, got %q", proven.FitLevel)
	}

	unproven := AnalyzeShape(shape, Params{VRAMTotalMB: 1, ConfiguredCtx: 4096, Unproven: true})
	if unproven.FitLevel != "no" {
		t.Fatalf("unproven gallery candidate: want honest \"no\", got %q (rescue must not apply)", unproven.FitLevel)
	}
	if unproven.MaxSafeCtx != 0 {
		t.Errorf("unproven \"no\" verdict must carry max_safe_ctx=0, got %d", unproven.MaxSafeCtx)
	}
}

// Same guard on the UnderConfigured flag: only a proven, deployed model's
// starved config is worth surfacing as a warning.
func TestParams_Unproven_NoUnderConfiguredFlag(t *testing.T) {
	shape := ModelShape{LayerCount: 32, EmbeddingLength: 4096, HeadCount: 32, HeadCountKV: 8, WeightBytes: 1 * 1024 * 1024 * 1024, TrainedCtx: 131072}

	proven := AnalyzeShape(shape, Params{VRAMTotalMB: 24000, ConfiguredCtx: 2048})
	if !proven.UnderConfigured {
		t.Fatal("proven configured model far below VRAM-achievable ceiling: want UnderConfigured=true")
	}

	unproven := AnalyzeShape(shape, Params{VRAMTotalMB: 24000, ConfiguredCtx: 2048, Unproven: true})
	if unproven.UnderConfigured {
		t.Fatal("unproven gallery candidate: want UnderConfigured=false, it has never loaded at any ctx")
	}
}
