# Add an Instella-MoE correctness harness

## Context

The Instella-MoE investigation established that a mis-implemented FarSkip graph, or a
mishandled YaRN `mscale`, produces **fluent but numerically wrong** output. The
`mscale_all_dim = 1.0` trap alone is a 1.874× error in the attention softmax scale — the
kind of bug that survives casual inspection because the model still writes plausible Go.

There is currently no harness in this repo for validating a GGUF against a Transformers
reference. Every past model on the fleet arrived as a pre-validated community GGUF, so the
question never came up.

Full analysis: `docs/investigations/instella-moe-w7800.md` §9, §12.

## Why

Three reasons this is its own change rather than a phase of the arch work:

1. **It is the acceptance gate for the arch change**, so it must exist first and be
   trustworthy independently.
2. **It generalises.** Any future architecture work — AMD's next MoE, a fork patch, a
   suspicious community GGUF — needs exactly this: tokenizer parity, first-token logit
   comparison, top-k rank agreement, and a failure-class attribution table.
3. **The staged-oracle trick is the single most valuable finding of the investigation** and
   deserves to be encoded rather than remembered. `configuration_instella_moe.py` exposes
   `attn_only_farskip` and `mlp_only_farskip`, which **decompose FarSkip's two independent
   changes**. Running the reference in three modes turns one opaque mismatch into a
   bisectable signal.

## What changes

Scripts under `scripts/correctness/` (or similar) plus a documented procedure:

- A pinned-revision reference runner (Transformers, BF16, greedy, on the W7800) recording
  input IDs, output IDs, first-token logits, top-32 logprobs for the first 8 positions,
  peak VRAM, and tok/s.
- A comparison tool with per-test tolerances, emitting a failure **class**, not just a
  pass/fail.
- The three-oracle mode matrix (as-shipped / `mlp_only_farskip` / `attn_only_farskip`).

Comparison is **F16 GGUF vs BF16 reference**, not Q8_0 vs BF16 — F16 fits in 48 GB with
the full 32K context (32,694 of 45,205 usable MiB), so quantization loss stays separable
from conversion error. Quantization delta is measured afterwards against the F16 GGUF.

## Non-goals

- Throughput benchmarking — see `add-instella-moe-skein-deployment`.
- Making this a generic multi-model regression suite. Build it for this job; generalise
  when a second caller appears.

## Risks

- **`trust_remote_code=True` executes code from the HF repo.** `modeling_instella_moe.py`
  and `configuration_instella_moe.py` were read during the investigation and are ordinary
  modelling code, but they must be **re-read at the pinned revision** before execution and
  the revision pinned explicitly in every call. Never run them unpinned.
- **ROCm is nondeterministic at temperature 0**
  ([llama.cpp #14727](https://github.com/ggml-org/llama.cpp/issues/14727)), so exact greedy
  string match is not a sound gate. Logit tolerances carry the weight; greedy divergence is
  a signal to investigate, not proof of a bug.
- The reference path caches **decompressed** per-head K/V (216 KiB/token) while llama.cpp
  caches the **latent** (28.7 KiB/token). Peak-VRAM figures are therefore not comparable
  between the two sides and must be reported separately, not diffed.
- Whether the `-Think` checkpoint emits a reasoning delimiter is **unverified** — no
  `<think>` token exists in the vocabulary and the chat template has none. Determining
  this is an output of Phase 1, not an assumption.
