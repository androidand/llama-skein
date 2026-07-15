# Spec delta: upgrade-api (fix-rdna3-flash-attn-wedge)

Internal ops endpoint (`POST /api/system/upgrade`) — not part of the
OpenAPI-generated wire contract.

## MODIFIED

### Binary replacement must never open-for-write onto a running executable

- Both upgrade methods (`prebuilt`, `source`) MUST replace the llama-server
  binary by writing the new binary to a temporary file in the same directory
  and atomically renaming it into place. They MUST NOT open the live server
  path for writing (this fails with `ETXTBSY` whenever a model is currently
  loaded, since the running process still has the old binary's inode mapped).
- Both methods MUST gracefully unload every locally-running model before the
  binary swap, so the new binary takes effect for the very next request
  rather than leaving an old (possibly wedged) process running until it
  happens to restart on its own.

### Prebuilt method must resolve a real, host-appropriate release asset

- The `prebuilt` method MUST resolve its download source based on the host:
  - When ROCm is detected and the host's GPU architecture has a published
    tailored bucket in `lemonade-sdk/llamacpp-rocm` (RDNA3/RDNA4 and other
    buckets they publish), that release MUST be preferred — it is built with
    the RDNA3-appropriate flash-attention kernel configuration by design, not
    as a workaround.
  - When ROCm is detected but the architecture has no lemonade-sdk tailored
    bucket, the method MUST fall back to upstream `ggml-org/llama.cpp`'s own
    generic ROCm release, and MUST surface a note that this build is not
    architecture-tailored.
  - When ROCm is not detected, the method MUST use upstream's plain CPU
    release.
- The method MUST resolve the actual release (by tag or "latest") via the
  GitHub releases API and select a real asset from that release's asset list
  — it MUST NOT assume a fixed, hardcoded download URL, since release asset
  naming and hosting organization can and do change over time.
- Archive extraction MUST use only the language's standard library (no
  dependency on an external `unzip`/`tar` binary being present on the host).
