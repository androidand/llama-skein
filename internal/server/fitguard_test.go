package server

import (
	"testing"

	"github.com/androidand/llama-skein/pkg/apicontract"
)

func TestSetCtxSizeInCmd(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		n    int
		want string
	}{
		{"bare --ctx-size", "llama-server --ctx-size 393216 -ngl 999", 65536, "llama-server --ctx-size 65536 -ngl 999"},
		{"-c alias", "llama-server -c 200000 --model m.gguf", 40000, "llama-server -c 40000 --model m.gguf"},
		{"equals form", "llama-server --ctx-size=131072", 8192, "llama-server --ctx-size=8192"},
		{"-c equals form", "llama-server -c=99999", 4096, "llama-server -c=4096"},
		{"absent → unchanged", "llama-server --model m.gguf -ngl 999", 4096, "llama-server --model m.gguf -ngl 999"},
	}
	for _, c := range cases {
		if got := setCtxSizeInCmd(c.cmd, c.n); got != c.want {
			t.Errorf("%s: setCtxSizeInCmd(%q,%d) = %q, want %q", c.name, c.cmd, c.n, got, c.want)
		}
	}
}

func TestConfidentNoFit(t *testing.T) {
	s := &Server{}
	mk := func(level apicontract.ModelFitFitLevel, vram, model *int) apicontract.ModelFit {
		return apicontract.ModelFit{FitLevel: level, VramTotalMb: vram, ModelMb: model}
	}
	cases := []struct {
		name string
		mf   apicontract.ModelFit
		want bool
	}{
		{"confident no", mk(apicontract.No, ptrOf(24576), ptrOf(35000)), true},
		{"no but VRAM unknown → not confident", mk(apicontract.No, nil, ptrOf(35000)), false},
		{"no but weights unknown → not confident", mk(apicontract.No, ptrOf(24576), nil), false},
		{"no but weights zero → not confident", mk(apicontract.No, ptrOf(24576), ptrOf(0)), false},
		{"fits", mk(apicontract.Good, ptrOf(24576), ptrOf(8000)), false},
		{"unknown level", mk(apicontract.Unknown, nil, nil), false},
	}
	for _, c := range cases {
		if got := s.confidentNoFit(c.mf); got != c.want {
			t.Errorf("%s: confidentNoFit = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestPreloadFitRefusal is a regression for the z4 wedge: qwythos-9b's
// startup preload passed modelLoadRefusal (FitLevel was "marginal", not
// "no") and permanently claimed ~40GB of a 48GB card, starving the other two
// configured models and wedging the GPU when a swap tried to evict it under
// load. Preload must hold to a stricter bar than a normal load.
func TestPreloadFitRefusal(t *testing.T) {
	cases := []struct {
		name       string
		mf         apicontract.ModelFit
		ok         bool
		wantRefuse bool
	}{
		{"marginal is refused", apicontract.ModelFit{FitLevel: apicontract.Marginal, Reason: ptrOf("fits only above the VRAM safety margin")}, true, true},
		{"tight is allowed", apicontract.ModelFit{FitLevel: apicontract.Tight}, true, false},
		{"good is allowed", apicontract.ModelFit{FitLevel: apicontract.Good}, true, false},
		{"perfect is allowed", apicontract.ModelFit{FitLevel: apicontract.Perfect}, true, false},
		{"no is left to modelLoadRefusal", apicontract.ModelFit{FitLevel: apicontract.No}, true, false},
		{"unconfident (ok=false) fails open", apicontract.ModelFit{FitLevel: apicontract.Marginal}, false, false},
		{"unknown level fails open", apicontract.ModelFit{FitLevel: apicontract.Unknown}, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason, refuse := preloadFitRefusal(c.mf, c.ok)
			if refuse != c.wantRefuse {
				t.Errorf("refuse=%v want %v", refuse, c.wantRefuse)
			}
			if refuse && reason == "" {
				t.Error("expected a non-empty reason when refusing")
			}
		})
	}
}

// TestCtxClampDecision_MarginalConfiguredStillClamps is a regression for the
// Rocky incident: a 27B model hand-configured with --ctx-size 262144 on a
// 24GB card (real numbers captured live from that host). Its FitLevel reads
// "marginal" (fit.go's safety net protects any *configured* model from ever
// reporting "no"), but VramRequiredMb (25536) genuinely exceeds VramTotalMb
// (24560) — the real overflow signal, carrying no such safety net.
// clampModelsToFit must still shrink it to MaxFitCtx instead of leaving it
// configured to crash-loop on every load, which the original FitLevel=="no"
// gate missed entirely (a configured model's FitLevel can never read "no").
func TestCtxClampDecision_MarginalConfiguredStillClamps(t *testing.T) {
	mf := apicontract.ModelFit{
		FitLevel:       apicontract.Marginal,
		ConfiguredCtx:  ptrOf(262144),
		MaxFitCtx:      ptrOf(71480),
		VramRequiredMb: ptrOf(25536),
		VramTotalMb:    ptrOf(24560),
		ModelMb:        ptrOf(16031),
		Reason:         ptrOf("runs at the configured context; VRAM estimate exceeds budget"),
	}
	clampTo, reason, unfit := ctxClampDecision(mf)
	if unfit {
		t.Fatalf("expected a clamp, not unfittable (reason=%q)", reason)
	}
	if clampTo != 71480 {
		t.Errorf("clampTo = %d, want 71480", clampTo)
	}
}

// TestCtxClampDecision_MTPGenuinelyMarginalIsNotRefused is a regression for a
// second Rocky model, hit while fixing the incident above: an MTP model
// running in production, genuinely fitting ("marginal" from the switch
// itself, not the configured-safety-net rescue — VramRequiredMb 22825 <
// VramTotalMb 24560). An earlier revision of this function gated on MaxFitCtx
// instead of VramRequiredMb/VramTotalMb; MaxFitCtx applies an EXTRA
// safety discount for MTP models (mtpExtraSafetyFrac) that rounds to 0 here
// even though the model demonstrably fits and serves traffic — that revision
// wrongly refused it outright. Real numbers captured live from Rocky.
func TestCtxClampDecision_MTPGenuinelyMarginalIsNotRefused(t *testing.T) {
	mf := apicontract.ModelFit{
		FitLevel:       apicontract.Marginal,
		ConfiguredCtx:  ptrOf(98304),
		MaxFitCtx:      nil, // 0/unset: MTP's extra discount goes negative here
		VramRequiredMb: ptrOf(22825),
		VramTotalMb:    ptrOf(24560),
		ModelMb:        ptrOf(18630),
		Reason:         ptrOf("fits only above the VRAM safety margin; reduce context"),
	}
	clampTo, reason, unfit := ctxClampDecision(mf)
	if unfit {
		t.Fatalf("expected this genuinely-fitting model to be left alone, got unfit (reason=%q)", reason)
	}
	if clampTo != 0 {
		t.Errorf("clampTo = %d, want 0 (already fits, nothing to clamp to)", clampTo)
	}
}

func TestCtxClampDecision(t *testing.T) {
	cases := []struct {
		name        string
		mf          apicontract.ModelFit
		wantClampTo int
		wantUnfit   bool
	}{
		{
			name:        "already fits, configured below max",
			mf:          apicontract.ModelFit{ConfiguredCtx: ptrOf(8192), MaxFitCtx: ptrOf(71480), VramRequiredMb: ptrOf(19000), VramTotalMb: ptrOf(24560), ModelMb: ptrOf(16031)},
			wantClampTo: 0,
		},
		{
			name:        "unconfigured model with room to spare is left alone",
			mf:          apicontract.ModelFit{MaxFitCtx: ptrOf(71480), VramRequiredMb: ptrOf(19000), VramTotalMb: ptrOf(24560), ModelMb: ptrOf(16031)},
			wantClampTo: 0,
		},
		{
			name:      "weights alone exceed VRAM, unfittable at any context",
			mf:        apicontract.ModelFit{ConfiguredCtx: ptrOf(4096), MaxFitCtx: ptrOf(0), VramRequiredMb: ptrOf(42000), VramTotalMb: ptrOf(8192), ModelMb: ptrOf(40000)},
			wantUnfit: true,
		},
		{
			name:        "VRAM not yet known → fail open",
			mf:          apicontract.ModelFit{ConfiguredCtx: ptrOf(262144), MaxFitCtx: ptrOf(0), ModelMb: ptrOf(16031)},
			wantClampTo: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clampTo, reason, unfit := ctxClampDecision(c.mf)
			if unfit != c.wantUnfit {
				t.Errorf("unfit = %v, want %v", unfit, c.wantUnfit)
			}
			if clampTo != c.wantClampTo {
				t.Errorf("clampTo = %d, want %d", clampTo, c.wantClampTo)
			}
			if unfit && reason == "" {
				t.Error("expected a non-empty reason when unfit")
			}
		})
	}
}

func TestModelLoadRefusal_UnfittableSet(t *testing.T) {
	s := &Server{unfittable: map[string]string{"big-model": "weights exceed memory"}}
	if reason, refuse := s.modelLoadRefusal("big-model"); !refuse || reason == "" {
		t.Errorf("expected refusal for unfittable model, got refuse=%v reason=%q", refuse, reason)
	}
	// A model not recorded and not sizable here (no cfg) must fail open.
	if _, refuse := s.modelLoadRefusal("unknown-model"); refuse {
		t.Error("unknown/un-sizable model must fail open (not refused)")
	}
}
