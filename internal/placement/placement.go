// Package placement plans where a model's weights should live — GPU, host
// RAM, or a hybrid split — before launch. It is pure planning: inputs are a
// backend-neutral model shape, memory budgets, and policy; the output is a
// Plan with the flag operations that realize it. No HTTP, no config I/O, no
// process management.
//
// Strategy (see openspec/changes/add-auto-hybrid-placement/design.md):
// prefer upstream llama.cpp fitting over reimplementation. For MoE models we
// pin --n-cpu-moe (deterministic, computed from exact expert tensor bytes)
// and hand the engine a --fit-target margin; for dense models we delegate the
// layer split entirely to the engine's --fit (leaving -ngl at its "auto"
// default). What upstream fitting does NOT do — and this package owns — is
// host-RAM feasibility: refusing a plan whose CPU-resident share the cgroup
// would OOM-kill.
package placement

import (
	"fmt"
	"strconv"

	"github.com/androidand/llama-skein/internal/config"
	"github.com/androidand/llama-skein/internal/fit"
	"github.com/androidand/llama-skein/internal/offload"
)

// Mode is the placement decision for one model.
type Mode string

const (
	// ModeGPU: fits fully in the GPU budget; command left untouched.
	ModeGPU Mode = "gpu"
	// ModeHybrid: needs host RAM; flag ops realize the split.
	ModeHybrid Mode = "hybrid"
	// ModeCPU: no viable GPU placement, but the host budget holds it.
	ModeCPU Mode = "cpu"
	// ModeRefuse: even the most conservative plan exceeds the safe budgets.
	ModeRefuse Mode = "refuse"
	// ModeCustom: the operator pinned placement flags; automation stays out.
	ModeCustom Mode = "custom"
	// ModeUnknown: not enough information to plan confidently (fail open —
	// the model runs exactly as configured).
	ModeUnknown Mode = "unknown"
)

// PerfClass is a qualitative expectation, never a tok/s prediction.
type PerfClass string

const (
	PerfNativeGPU      PerfClass = "native-gpu"
	PerfFastHybrid     PerfClass = "fast-hybrid"
	PerfCPUBoundHybrid PerfClass = "cpu-bound-hybrid"
	PerfCPUOnly        PerfClass = "cpu-only"
)

// Load-deadline sizing. Weights have to be read from disk (or faulted in
// from page cache) before a model can answer a health check, so the deadline
// has to scale with the model, not sit at one global constant. The z4
// acceptance run hit exactly this: a 91 GB model was killed at the 120 s
// default mid-load, repeatedly, and looked like a broken model rather than
// one that simply needed longer.
//
// loadSecondsPerGB is deliberately pessimistic (a cold spinning-disk read,
// not a warm page-cache one): this is a deadline for giving up, so being
// generous costs only a slower failure, while being tight costs a model
// that can never load at all.
const (
	loadSecondsPerGB  = 12
	loadBaseSeconds   = 120
	loadMaxSeconds    = 3600
	hybridLoadPenalty = 1.5 // CPU-side expert setup on top of the read
	bytesPerGigabyte  = 1 << 30
)

// LoadDeadlineSeconds is the health-check timeout a model of this size and
// placement needs. Callers raise a configured timeout to this floor; they
// never lower one an operator chose.
func LoadDeadlineSeconds(weightBytes int64, mode Mode) int {
	if weightBytes <= 0 {
		return 0
	}
	gb := float64(weightBytes) / bytesPerGigabyte
	secs := float64(loadBaseSeconds) + gb*loadSecondsPerGB
	if mode == ModeHybrid || mode == ModeCPU {
		secs *= hybridLoadPenalty
	}
	if secs > loadMaxSeconds {
		secs = loadMaxSeconds
	}
	return int(secs)
}

// fastHybridMaxHostFrac splits fast-hybrid from cpu-bound-hybrid: with up to
// ~a third of the weights in host RAM, decode is usually still GPU-paced;
// beyond that, host memory bandwidth dominates every token.
const fastHybridMaxHostFrac = 0.35

// Inputs is everything the planner needs. The caller resolves budgets
// (whole-GPU semantics for exclusive swap groups, cgroup-effective host
// figures) — the planner does not read hardware.
type Inputs struct {
	Shape fit.ModelShape

	// Launch parameters the plan is computed against.
	ConfiguredCtx int // 0 = model trained default
	ParallelSlots int
	KCacheBits    float64 // 0 = f16
	VCacheBits    float64

	// Budgets in MB. VRAMBudgetMB is what this model gets once resident
	// (total for exclusive-group models). HostAvailableMB/HostTotalMB are
	// the cgroup-effective figures; 0 = unknown.
	VRAMBudgetMB    int
	HostAvailableMB int
	HostTotalMB     int

	// PinnedPlacement is true when the command already pins any
	// placement-affecting flag (-ngl / --n-cpu-moe / --cpu-moe /
	// --override-tensor / --tensor-split): automation must not touch it.
	PinnedPlacement bool

	Policy config.PlacementConfig
}

// Estimate is the plan's memory expectation in MB.
type Estimate struct {
	GPUMB  int
	HostMB int
	KVMB   int
}

// Plan is the planner's decision for one model.
type Plan struct {
	Mode      Mode
	FlagOps   []offload.FlagOp
	NCpuMoe   int // layers whose experts move to CPU (hybrid MoE plans)
	Estimate  Estimate
	PerfClass PerfClass
	Reason    string
	// Confident is true when the plan is backed by known budgets and a
	// parsed model shape. Only confident ModeRefuse plans may refuse a load;
	// anything else fails open.
	Confident bool
}

// Applies reports whether the plan carries flag ops to apply.
func (p Plan) Applies() bool { return len(p.FlagOps) > 0 }

// Plan decides the placement for one model. It never returns an error: a
// plan that cannot be computed confidently comes back as ModeUnknown (or
// ModeCustom/ModeGPU no-ops), and callers leave the model untouched.
func Compute(in Inputs) Plan {
	if in.PinnedPlacement {
		return Plan{Mode: ModeCustom, PerfClass: PerfNativeGPU,
			Reason: "command pins placement flags; automatic placement stays out"}
	}
	switch in.Policy.EffectiveMode() {
	case config.PlacementModeGPU:
		return Plan{Mode: ModeGPU, PerfClass: PerfNativeGPU,
			Reason: "placement.mode is gpu; commands are never rewritten"}
	}
	if in.Shape.WeightBytes <= 0 {
		return Plan{Mode: ModeUnknown, Reason: "model weight size unknown; cannot plan placement"}
	}
	if in.VRAMBudgetMB <= 0 {
		return Plan{Mode: ModeUnknown, Reason: "VRAM budget unknown (telemetry warming up); cannot plan placement"}
	}

	gpuReserve := in.Policy.GpuReserveMB(in.VRAMBudgetMB)
	vramBudget := in.VRAMBudgetMB - gpuReserve
	hostBudget := 0
	if in.HostAvailableMB > 0 && in.HostTotalMB > 0 {
		hostBudget = in.HostAvailableMB - in.Policy.HostReserveMB(in.HostTotalMB)
		if hostBudget < 0 {
			hostBudget = 0
		}
	}

	// checkHost=false scores the GPU side only — used inside the MoE search,
	// where the host share grows as the GPU share shrinks and a combined
	// verdict would not be monotonic in n.
	analyze := func(p fit.Params, checkHost bool) fit.Result {
		p.KCacheBits = in.KCacheBits
		p.VCacheBits = in.VCacheBits
		p.ConfiguredCtx = in.ConfiguredCtx
		p.ParallelSlots = in.ParallelSlots
		p.VRAMTotalMB = vramBudget
		if checkHost {
			p.HostBudgetMB = hostBudget
		}
		p.Unproven = true // plan honestly: no configured-model rescue
		return fit.AnalyzeShape(in.Shape, p)
	}

	// A card smaller than its own reserve is effectively unusable for
	// weights: skip straight to the CPU-only consideration.
	if vramBudget <= 0 {
		if plan, ok := planCPUOnly(in, hostBudget); ok {
			return plan
		}
		return Plan{Mode: ModeRefuse, Confident: hostBudget > 0,
			Reason: "GPU budget is below the configured reserve and the model does not fit the safe host budget"}
	}

	// 1. Full GPU?
	full := analyze(fit.Params{}, true)
	if fits(full) {
		return Plan{
			Mode:      ModeGPU,
			Estimate:  Estimate{GPUMB: full.VRAMRequiredMB, KVMB: kvMB(full)},
			PerfClass: PerfNativeGPU,
			Confident: true,
			Reason:    "fits fully in the GPU budget",
		}
	}
	if full.FitLevel == "unknown" {
		return Plan{Mode: ModeUnknown, Reason: "fit engine could not size the model: " + full.Reason}
	}

	// Anything past this point places weights in host RAM; without a known
	// host budget that is a guess, not a plan.
	if hostBudget <= 0 {
		return Plan{Mode: ModeUnknown,
			Reason: "model exceeds the GPU budget and the host memory budget is unknown; not planning a hybrid placement blind"}
	}

	// 2. Hybrid MoE: walk --n-cpu-moe up until the GPU side fits, then verify
	// the host side (AnalyzeShape checks both).
	if in.Shape.IsMoE && in.Shape.ExpertBytesTotal > 0 && in.Shape.LayerCount > 0 {
		if plan, ok := planMoE(in, hostBudget, analyze); ok {
			return plan
		}
	} else if !in.Shape.IsMoE {
		// 2b. Hybrid dense: delegate the layer split to the engine's own
		// fitting (-ngl stays unset => auto). We only gate feasibility: the
		// spill (weights that cannot sit in VRAM) must fit the host budget.
		spillMB := int(in.Shape.WeightBytes/mib) - vramBudget
		if spillMB <= hostBudget {
			return Plan{
				Mode:      ModeHybrid,
				FlagOps:   fitTargetOps(gpuReserve, in.Policy.MinCtx()),
				Estimate:  Estimate{GPUMB: vramBudget, HostMB: max0(spillMB), KVMB: kvMB(full)},
				PerfClass: hybridPerfClass(spillMB, int(in.Shape.WeightBytes/mib)),
				Confident: true,
				Reason: fmt.Sprintf("dense model exceeds the GPU budget by ~%d MB; delegating the layer split to the engine's --fit with a %d MB reserve",
					max0(spillMB), gpuReserve),
			}
		}
	}

	// 3. CPU-only, if the budget allows.
	if plan, ok := planCPUOnly(in, hostBudget); ok {
		return plan
	}

	return Plan{
		Mode:      ModeRefuse,
		Confident: true,
		Reason: fmt.Sprintf("weights (%d MB) exceed VRAM budget (%d MB) and safe host budget (%d MB) in every placement",
			int(in.Shape.WeightBytes/mib), vramBudget, hostBudget),
	}
}

// planCPUOnly plans -ngl 0 when weights + KV + overhead fit the host budget.
// Pure arithmetic — the fit oracle cannot score a zero-VRAM host.
func planCPUOnly(in Inputs, hostBudget int) (Plan, bool) {
	if hostBudget <= 0 {
		return Plan{}, false
	}
	// KVBytesPerToken is computed even when the oracle cannot reach a
	// verdict, so borrow it for the host-side KV estimate.
	probe := fit.AnalyzeShape(in.Shape, fit.Params{
		KCacheBits: in.KCacheBits, VCacheBits: in.VCacheBits,
		ConfiguredCtx: in.ConfiguredCtx, VRAMTotalMB: 1,
	})
	ctx := in.ConfiguredCtx
	if ctx <= 0 {
		ctx = int(in.Shape.TrainedCtx)
	}
	weightsMB := int(in.Shape.WeightBytes / mib)
	kvTotalMB := int(probe.KVBytesPerToken * int64(ctx) / mib)
	requiredMB := weightsMB + kvTotalMB + weightsMB/12 // ~8% overhead
	if requiredMB > hostBudget {
		return Plan{}, false
	}
	return Plan{
		Mode:      ModeCPU,
		FlagOps:   []offload.FlagOp{{Name: "--n-gpu-layers", Value: "0"}},
		Estimate:  Estimate{HostMB: weightsMB, KVMB: kvTotalMB},
		PerfClass: PerfCPUOnly,
		Confident: true,
		Reason:    "no viable GPU placement; weights fit the host budget CPU-only",
	}, true
}

// planMoE finds the smallest --n-cpu-moe whose GPU share fits, verified by
// the same fit oracle used everywhere else. The search scores the GPU side
// only (monotonic in n: more experts off the card can only shrink the GPU
// requirement); the host side is then verified once at the minimal n — the
// minimal fitting n has the smallest host share of any fitting n, so a host
// breach there is a breach everywhere. ok=false when no expert count rescues
// the model (caller falls through to CPU-only/refuse).
func planMoE(in Inputs, hostBudget int, analyze func(fit.Params, bool) fit.Result) (Plan, bool) {
	layers := int(in.Shape.LayerCount)
	lo, hi := 1, layers
	found := -1
	for lo <= hi {
		mid := (lo + hi) / 2
		if fits(analyze(fit.Params{NCpuMoe: mid}, false)) {
			found = mid
			hi = mid - 1
		} else {
			lo = mid + 1
		}
	}
	if found < 0 {
		// Even all experts on CPU doesn't fit the GPU side — not a hybrid
		// MoE candidate.
		return Plan{}, false
	}
	res := analyze(fit.Params{NCpuMoe: found}, true)
	if res.HostResidentMB > hostBudget {
		return Plan{}, false
	}
	// Pinning --n-cpu-moe sets tensor_buft_overrides, and the engine then
	// abandons its own fitting entirely ("tensor_buft_overrides already set
	// by user, abort") — so --fit-target would be silently ignored and the
	// plan must be complete on its own. Verified on z4 (llama.cpp 956973c),
	// where relying on the engine to finish the job left 57 GB on a 48 GB
	// card and OOM'd. The reserve is already inside vramBudget, so the
	// pinned split alone respects it.
	ops := []offload.FlagOp{{Name: "--n-cpu-moe", Value: strconv.Itoa(found)}}
	return Plan{
		Mode:      ModeHybrid,
		FlagOps:   ops,
		NCpuMoe:   found,
		Estimate:  Estimate{GPUMB: res.VRAMRequiredMB, HostMB: res.HostResidentMB, KVMB: kvMB(res)},
		PerfClass: hybridPerfClass(res.HostResidentMB, res.ModelMB),
		Confident: true,
		Reason: fmt.Sprintf("experts of the first %d/%d layers move to CPU (~%d MB host-resident); GPU share ~%d MB fits the budget",
			found, layers, res.HostResidentMB, res.VRAMRequiredMB),
	}, true
}

// fitTargetOps hands the engine's own fitting our reserves: --fit-target is
// the per-device free-VRAM margin llama.cpp maintains when it adjusts unset
// arguments (--fit defaults to on), --fit-ctx the floor it may shrink an
// unset context to. Harmless on commands that pin everything; load-bearing
// whenever the engine still has degrees of freedom.
func fitTargetOps(gpuReserveMB, minCtx int) []offload.FlagOp {
	return []offload.FlagOp{
		{Name: "--fit-target", Value: strconv.Itoa(gpuReserveMB)},
		{Name: "--fit-ctx", Value: strconv.Itoa(minCtx)},
	}
}

func hybridPerfClass(hostMB, modelMB int) PerfClass {
	if modelMB <= 0 || float64(hostMB)/float64(modelMB) <= fastHybridMaxHostFrac {
		return PerfFastHybrid
	}
	return PerfCPUBoundHybrid
}

// fits treats tight/good/perfect as fitting; marginal is not a plan we
// *choose*, it is what deployed reality gets rescued to.
func fits(r fit.Result) bool {
	switch r.FitLevel {
	case "perfect", "good", "tight":
		return true
	}
	return false
}

func kvMB(r fit.Result) int {
	return r.KVMBAtMaxSafeCtx
}

func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

const mib = 1024 * 1024
