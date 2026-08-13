# Spec: Muse Glimmer 30B GGUF Support

## Problem

Muse Glimmer 30B ships as a model package with three artifacts (main GGUF, DFlash drafter GGUF, mmproj GGUF), but llama-skein currently treats models as single GGUF files and does not discover, fit, or run companion artifacts.

## Goal

Enable llama-skein to pull, fit, and run the Muse Glimmer package that exists on HuggingFace today, while implementing companion-artifact capability generically for future models.

## Prerequisite

Before implementing, verify llama.cpp's own HF resolver behavior:

1. Build llama.cpp from main
2. Run `llama-server -hf meta-models/Muse-Glimmer-30B-GGUF`
3. Determine: does it auto-download mmproj? DFlash? What flags does it construct?
4. If llama.cpp handles companion resolution, delegate — do not reimplement

## Changes

### 1. Companion Artifact Discovery

When a model is pulled from a HuggingFace repository, the system discovers sibling GGUF files that serve as companion artifacts.

- `mmproj-*.gguf` → projector companion
- `dflash-*.gguf` → draft model companion
- Discovered companions are associated with the main model by repository proximity

The operation API supports specifying artifacts explicitly OR discovering them automatically from the source repository.

### 2. Fit Calculation Includes Companions

The fit engine accounts for all package components when computing VRAM requirements:

- Main model weights
- Draft model weights (if companion present)
- mmproj weights (if companion present)
- KV cache (with Muse Glimmer's GQA 16:1 ratio and hybrid attention interval)
- Runtime overhead

### 3. Runtime Command Includes Companion Flags

The system constructs llama-server commands with the correct flags for companion artifacts:

- `--model-draft <path>` for DFlash drafter
- `--mmproj <path>` for multimodal projector
- `--spec-type draft-dflash` when DFlash is present (distinct from `draft-mtp`)

### 4. Context Length Override

Muse Glimmer GGUF metadata declares context length 131072, but the model supports 262144. The system supports `--override-kv muse-glimmer.context_length=int:262144` to unlock full context when requested.

### 5. Model Config Stores Companions

The model configuration records companion artifact paths alongside the main model, so restarts and state queries include the full package.

## Out of Scope

- Automatic quantization selection — caller decides which variant
- General model recipe extraction from HF config files — future work
- Reimplementing llama.cpp's HF resolver — delegate where possible

## Verification

1. Pull Muse Glimmer from HuggingFace — main + DFlash + mmproj are discovered and downloaded
2. Fit calculation for a 48 GB card accounts for all three artifacts
3. Generated command includes `--model-draft` and `--mmproj` flags
4. DFlash speculative decoding works (spec-type: draft-dflash)
5. Multimodal input works with mmproj
6. VRAM usage matches predictions
