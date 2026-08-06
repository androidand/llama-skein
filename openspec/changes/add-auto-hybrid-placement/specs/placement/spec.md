# Spec delta: placement (add-auto-hybrid-placement)

## ADDED

### Placement planning

- For every configured llama.cpp model, the server MUST compute a placement
  plan from the model's GGUF-derived shape (including exact expert tensor
  bytes for MoE models), the GPU budget (exclusive-swap-group models budget
  against total VRAM; shared models against live free VRAM; never above
  physical total), and the effective host memory limit — each minus its
  configured reserve. Swap MUST never count toward any budget.
- The plan's mode MUST be one of:
  - `gpu` — fits fully in the GPU budget at the configured context,
  - `hybrid` — requires host RAM (MoE: pinned `--n-cpu-moe` derived from
    exact expert bytes plus `--fit-target`; dense: delegated to the engine's
    automatic fitting with `--fit-target`/`--fit-ctx` set from policy),
  - `cpu` — no viable GPU placement but fits the host budget,
  - `refuse` — even the most conservative plan (all experts on CPU, minimum
    context) exceeds the safe budgets.
- A model whose `cmd` already pins any placement-affecting flag (`-ngl`,
  `--n-cpu-moe`, `--cpu-moe`, `--override-tensor`, `--tensor-split`) MUST be
  treated as `custom`: automatic placement MUST NOT modify it.
- Plans MUST include a memory estimate (GPU bytes, host bytes, KV bytes), a
  qualitative performance class (`native-gpu | fast-hybrid | cpu-bound-hybrid
  | cpu-only`), and a human-readable reason. The server MUST NOT fabricate
  tokens-per-second predictions.
- The planner MUST NOT enable KV-cache quantization unless
  `placement.allowKvQuantization` is true, and MUST NOT enable or disable
  speculative/draft models; configured draft flags are counted as GPU cost.

### Automatic application

- Plans are applied at boot/reload and after config writes by rewriting the
  model command **in memory only** (original preserved); placement flags MUST
  never be persisted to the config file by the planner.
- Application MUST fail open: when a plan cannot be computed confidently
  (missing tensor table, unknown VRAM or host limit, unmodeled backend), the
  model runs exactly as configured.
- Because placement is per-model and in-memory, a model whose plan is `gpu`
  MUST run with its configured command unchanged — loading a small model
  after a hybrid one requires no reversal step.
- Where a `llama-fit-params` utility is available beside the engine binary,
  the server SHOULD preflight the planned arguments with it and expose the
  resulting effective arguments; its absence MUST NOT block a load.

### Policy

- A top-level `placement:` config block controls: `mode`
  (`auto|gpu|hybrid|cpu`, default `auto`; non-auto modes fail rather than
  silently fall back), host/GPU reserves (defaults: host `max(12 GiB, 10% of
  effective total)`, GPU `max(2 GiB, 5% of VRAM)`), `minimumContext`, and
  `allowKvQuantization` (default false). Reserves MUST be configurable and
  reported alongside every plan.

### Visibility

- Fit responses (`/api/fit`, `/api/fit/{model}`, `/api/fit/hypothetical`) and
  `/v1/models` runtime hints MUST expose the effective placement: mode,
  estimated GPU/host split, applied flags, performance class, and reason.
  Hypothetical/per-variant verdicts MUST distinguish `hybrid` from `no` so a
  90 GB quant on a 48 GB card ranks as loadable-with-caveats, not unloadable.
- The requested mode and the effective mode MUST both be reported, since
  `auto` resolves to a concrete mode at plan time.

## MODIFIED

### Load refusal gate (from add-fit-load-guard)

- The fit guard gains hybrid placement as a remedy between "shrink context"
  and "refuse": a model larger than VRAM MUST NOT be refused or marked
  unfittable when a confident `hybrid` or `cpu` plan exists within the safe
  budgets. 507 refusal now requires confident infeasibility of the most
  conservative plan.
