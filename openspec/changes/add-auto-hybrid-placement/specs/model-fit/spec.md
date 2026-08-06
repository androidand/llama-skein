# Spec delta: model-fit (add-auto-hybrid-placement)

## MODIFIED

### Offload-aware fit analysis

- The fit engine MUST model CPU-offloaded weights: expert tensors placed on
  CPU (via `--n-cpu-moe`/`--cpu-moe`/`--override-tensor`) and layers kept off
  the GPU (via `-ngl`) are subtracted from the VRAM requirement and budgeted
  against the effective host memory limit instead.
- `fitForModel` MUST parse the model command's offload flags (through the
  backend's offload translator) so a hand-offloaded or auto-placed model is
  scored by its actual placement, not as fully GPU-resident. The
  `fit.go` "configured model rescued to marginal" escape hatch is narrowed
  accordingly: offloaded models get an honest verdict, not a forgiven one.
- Fit results MUST report the GPU-resident and host-resident weight split.
- `RecommendCpuMoe` MUST budget the KV cache with the fit engine's
  cache-type-aware math (single source of truth); the FP16-only estimate is
  retired from the recommendation path.
