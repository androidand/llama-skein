# Proposal: RDNA3 flash-attention wedge — root cause, fix, and durable upgrade support

## Why

z4 (an AMD Radeon Pro W7800, 48GB, gfx1100/RDNA3 host) repeatedly became
unresponsive: a loaded model would report `state: ready` but produce zero
tokens, with requests hanging indefinitely. The signature, confirmed across
many incidents: `gpu_busy_percent` pinned at 100%, `mem_busy_percent`
(memory-controller activity, via `rocm-smi --showmemuse`) stuck at 11-14% —
versus 70%+ observed during genuine generation on the same hardware. The
`llama-server` process stayed alive at ~0% CPU. This was the dominant
operational complaint across several days.

Everything built earlier in this effort (serialize-to-slot-count, the request
hard-timeout, `cancelBusySlots`, the bounded `killProcess` wait, the standalone
GPU-stall watchdog) is *recovery* — it detects and restarts a wedged backend
faster. None of it addresses why the backend wedges in the first place.

## Root cause

- Proxmox host `dmesg` never logged a single amdgpu ring-timeout / GPU-reset /
  fence-timeout across any incident — ruling out a genuine kernel/hardware GPU
  hang, and specifically ruling out the LXC/container-passthrough MES-doorbell
  hang family (which always logs `MES failed to respond`). A sibling host
  (`proxmox`, also LXC-passthrough on Proxmox) is stable, confirming the
  container mechanism itself isn't the cause.
- z4 ran the stock `ggml-org/llama.cpp` ROCm build. llama-skein's own tuning
  database recommends `flash_attn: true` for gfx1100, sourced from a GitHub
  discussion and explicitly marked `"verified": false"` in the profile's own
  notes.
- Qwen3.6 (the model family that reliably wedged) uses GQA with
  **head_dim = 256**. RDNA3's WMMA-based flash-attention kernel is the
  suspected unstable path at this head size — llama.cpp's own dispatch code
  has no RDNA-specific head-dim gating, so nothing stops the kernel from being
  selected regardless.
- **The clinching evidence:** `lemonade-sdk/llamacpp-rocm`, an actively
  maintained, AMD-adjacent nightly build project that explicitly targets
  gfx110X/RDNA3 (including the W7800), builds every RDNA3/RDNA4 target with
  `-DGGML_HIP_ROCWMMA_FATTN=OFF` **by design** — confirmed from their own
  `docs/manual_instructions.md`. This is their deliberate, correct, tailored
  configuration for this GPU family, not a workaround.

## What

- **z4 runs `lemonade-sdk/llamacpp-rocm`'s prebuilt `gfx110X` build** instead
  of the stock ggml-org build. Validated live: full-context load (no OOM),
  sustained real generation at 72-73% memory-controller activity (vs. 11-14%
  during every prior wedge), the exact concurrent-request pattern that
  originally triggered the reports completing cleanly, and all three z4
  models verified through real llama-skein routing (config, macros, env,
  swap-eviction).
- **`POST /api/system/upgrade`'s `prebuilt` method is now ROCm/RDNA3-aware,
  so this fix is durable rather than a one-off manual swap:**
  - Resolves the right release source per host: ROCm + a GPU arch
    lemonade-sdk publishes a tailored bucket for (gfx110X, gfx120X, gfx103X,
    and their exactly-named archs) → their build; ROCm + an untailored arch →
    upstream's own generic ROCm build (clearly logged as untailored, since it
    may carry the same instability); no ROCm → upstream's plain CPU build.
  - Extraction uses only the standard library (`archive/zip`, `archive/tar`,
    `compress/gzip`) — no external `unzip`/`tar` dependency, since one can't
    be assumed present (z4's LXC shipped without either).
  - Verified live: `POST /api/system/upgrade {"method":"prebuilt","ref":"latest"}`
    against z4 today correctly re-resolves and re-installs the same
    gfx110X-tailored build with zero manual intervention.
- **Two real bugs fixed in the upgrade API**, found while validating the fix:
  - `upgradeFromSource` copied the new binary directly onto the live
    `serverPath` (open-for-write onto a currently-executing file) — fails
    with `ETXTBSY` ("text file busy") whenever a model is loaded during
    upgrade, exactly as hit live. Fixed with `safeReplaceBinary`: write to a
    temp file in the same directory, then atomically rename into place — safe
    regardless of whether the old binary is currently running.
  - `upgradePrebuilt`'s hardcoded download URL pointed at the renamed
    `ggerganov` org and assumed a bare `llama-server` binary asset exists —
    modern llama.cpp releases don't publish one (only platform/variant
    tarballs/zips). This method was very likely non-functional for any host
    before this change.
  - Both upgrade paths now gracefully unload running models before the binary
    swap, so the new binary takes effect immediately rather than an old
    (possibly wedged) process lingering until its next natural restart.

## Constraints

- `/api/system/upgrade` is an internal ops endpoint, not part of the
  OpenAPI-generated wire contract (confirmed: absent from
  `contracts/llama-skein.openapi.json`) — no spec-first regen needed for these
  changes.
- The lemonade-sdk gfx-bucket mapping (`lemonadeGfxBucket`) only covers
  architectures currently published by their project; an untailored fallback
  is deliberate rather than blocking the upgrade outright.

## Non-goals

- Migrating rocky (also gfx1100/RDNA3) to the lemonade-sdk build — same
  latent risk, hasn't been reported as wedging as visibly; preventative, not
  done here.
- Migrating proxmox (gfx1201, different arch) — currently stable, lower
  priority, would need its own lemonade-sdk asset if pursued.
- Changing llama-skein's tuning database's `flash_attn: true` recommendation
  for gfx1100 — worth reconsidering separately, not done here.
