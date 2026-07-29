# Tasks: Add an Instella-MoE correctness harness

Target repo: **llama-skein** (this repo). Runs on z4 LXC 102.
Reference: `docs/investigations/instella-moe-w7800.md` §9 (oracle setup) and §12 (the
comparison ladder + failure-class attribution table).

## Phase 1 — reference environment
- [ ] 1. `pct resize 102 rootfs +200G` if not already done — the full run needs ~88 GiB of
      the 99.3 GiB free.
- [ ] 2. **Re-read `modeling_instella_moe.py` and `configuration_instella_moe.py` at
      revision `e67a4a54d81b19692ec85ea1d1c777aa5c0bfd83` before executing anything.**
      Record a note confirming the review. This is a hard gate, not a formality.
- [ ] 3. Install into a venv in LXC 102:
      `torch` from `https://download.pytorch.org/whl/rocm7.0` (4.8 GB,
      `torch-2.10.0+rocm7.0-cp312`), then `transformers==4.57.1` (the version the model
      declares), `safetensors`, `huggingface_hub`, `accelerate`, `numpy`.
- [ ] 4. Commit a lockfile (`pip freeze`) and record the ROCm version (7.2.4) and HIP
      version (7.2.53211).
- [ ] 5. Download BF16 weights **once** (29.6 GiB) with the revision pinned. Do not
      re-download for later phases.

## Phase 2 — reference oracle
- [ ] 6. Reference runner: `revision=` pinned, `trust_remote_code=True`,
      `torch_dtype=torch.bfloat16`, `device_map="cuda:0"`, `do_sample=False`,
      `temperature=None`, `top_p=None`, `top_k=None`, fixed `max_new_tokens`,
      `PYTHONHASHSEED=0`.
- [ ] 7. Three prompts: (a) the Go/context.Context worker-pool prompt from the brief,
      (b) "The capital of France is" as a gross-error canary, (c) a single-token prompt
      with `max_new_tokens=1` as the pure forward-pass probe.
- [ ] 8. Record per prompt: tokenized input IDs, generated output IDs, generated text,
      **full first-token logit vector**, top-32 IDs + logprobs for the first 8 positions,
      peak VRAM (`torch.cuda.max_memory_allocated`), wall-clock tok/s.
- [ ] 9. Run each prompt twice with `use_cache=True` and once with `use_cache=False`;
      confirm the cache path agrees with itself.
- [ ] 10. **Determine empirically whether the model emits any reasoning delimiter**, and
      what it looks like. Record verbatim. Feeds the `reasoning:` and `--jinja` decisions
      in `add-instella-moe-skein-deployment`.
- [ ] 11. Generate the **three oracles** by overriding config:
      A = as-shipped; B = `mlp_only_farskip=True` (parallel residual only);
      C = `attn_only_farskip=True` (far-skip only). Store all three.
- [ ] 12. Commit oracle artifacts (IDs + logits as compressed npz, text as-is) so the
      comparison is reproducible without a GPU.

## Phase 3 — comparison tool
- [ ] 13. Comparison harness taking a `llama-server` endpoint (or `llama-perplexity` /
      a logits dump) plus an oracle artifact, emitting a **failure class** per §12.
- [ ] 14. Implement the ladder with tolerances:
      1. tokenizer parity — exact IDs
      2. chat-template render — exact bytes
      3. single-token full logits — cosine ≥ 0.9999, max abs diff ≤ 0.05
      4. top-32 IDs/logprobs, first 8 positions — top-1 identical, top-8 set identical,
         logprob diff ≤ 0.02
      5. greedy 128 tokens — report, **do not gate** (ROCm nondeterminism, #14727)
      6. repeat ×5 — self-consistency
      7. staged A/B/C — same tolerances as 3–4
      8. long context 1K/4K/8K/16K/32K, final-position logits — cosine ≥ 0.999
      9. Q8_0 vs **F16 GGUF** perplexity delta — report, do not gate
      10. router observability: top-6 expert IDs per layer — identical for ≥ 95% of tokens
- [ ] 15. Encode the failure-class attribution table from §12 so a failure prints its
      likely cause. In particular: a near-constant *scale* error on test 3 must print
      "check the mscale trap — expected effective scale 0.16562688".
- [ ] 16. Report peak VRAM for both sides **separately, never diffed** (latent vs
      decompressed KV layouts are not comparable).

## Phase 4 — gate
- [ ] 17. Run the full ladder against the F16 GGUF from
      `add-instella-moe-gguf-conversion` + the arch build from
      `add-instella-moe-llamacpp-arch`.
- [ ] 18. Tests 1–4, 6, 7, 8 must pass before any quantization or deployment work starts.
- [ ] 19. Record results in `docs/investigations/instella-moe-w7800.md` §9, replacing the
      "no inference was run" placeholder. **Do not fabricate or extrapolate any number.**
