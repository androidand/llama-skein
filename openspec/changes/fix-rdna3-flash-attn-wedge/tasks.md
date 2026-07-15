# Tasks: fix-rdna3-flash-attn-wedge

## Root cause investigation

- [x] 1. Confirm no kernel-level amdgpu fault across incidents (`dmesg` on the
       Proxmox host, fresh buffer verified via recent entries).
- [x] 2. Rule out the LXC/container-passthrough hang family (always logs
       `MES failed to respond`; z4 never did). Confirm via a stable sibling
       host (`proxmox`, also LXC-passthrough) that the container mechanism
       itself isn't the cause.
- [x] 3. Correct earlier wrong assumption: z4's GPU is an AMD Radeon Pro
       W7800 48GB (gfx1100/RDNA3), not the same card as rocky.
- [x] 4. Identify Qwen3.6's head_dim=256 and llama.cpp's lack of RDNA-specific
       head-dim gating in `ggml-cuda/fattn.cu` as the suspected mechanism.
- [x] 5. Find the clinching evidence: `lemonade-sdk/llamacpp-rocm` builds
       every RDNA3/RDNA4 target with `-DGGML_HIP_ROCWMMA_FATTN=OFF` by
       design (confirmed from their `docs/manual_instructions.md`).

## Fix — deploy lemonade-sdk build to z4

- [x] 6. Install `cmake`/`git`/`unzip` on z4's LXC (none present).
- [x] 7. Download + extract `lemonade-sdk/llamacpp-rocm`'s `gfx110X` release
       to `/opt/llamacpp-rocm-gfx110X/` on z4.
- [x] 8. Update z4's `config.yaml` (macro + per-model `cmd:`/`env:`) to use
       the new binary; applied via `POST /api/config/reload`.
- [x] 9. Validate: clean-VRAM full-context load (no OOM), sustained real
       generation (72-73% mem-activity vs. 11-14% wedge), the original
       concurrent-request trigger completing cleanly, all 3 z4 models
       verified via real llama-skein routing.

## Durable fix — make the upgrade API ROCm/RDNA3-aware

- [x] 10. Fix `upgradeFromSource`'s ETXTBSY bug: `safeReplaceBinary`
        (write-temp + atomic rename) replaces the direct
        open-for-write-onto-live-binary pattern. Applied to both upgrade
        paths.
- [x] 11. Add graceful `unloadAllModels` before the binary swap in both
        upgrade paths.
- [x] 12. Fix `upgradePrebuilt`'s broken download (`ggerganov` org, bare-binary
        assumption — likely non-functional for any host). Rewrote to resolve
        the correct GitHub release + asset via the real API and asset naming.
- [x] 13. Add `lemonadeGfxBucket` + `resolvePrebuiltSource`: ROCm + a
        lemonade-sdk-tailored arch → their build (preferred); ROCm + an
        untailored arch → upstream's generic ROCm build (logged as
        untailored); no ROCm → upstream's plain CPU build.
- [x] 14. Native Go archive extraction (`archive/zip`, `archive/tar`,
        `compress/gzip`) — no external `unzip`/`tar` dependency.
- [x] 15. Verify bundled libs need no `LD_LIBRARY_PATH` wiring: confirmed live
        the lemonade-sdk binary carries `RPATH=$ORIGIN` (runs correctly with
        a fully stripped environment when co-located with its libs).
- [x] 16. 16 new tests: `lemonadeGfxBucket`, `resolvePrebuiltSource` (incl. a
        real regression the tests caught — the matcher initially accepted
        the Windows asset as readily as Ubuntu's), `selectReleaseAsset`,
        `stripTopLevelDir`, zip/tar.gz round-trip extraction, path-escape
        (zip-slip) rejection.
- [x] 17. `go build ./...`, full suite (1012 tests), commit, push.
- [x] 18. Live end-to-end validation: `POST /api/system/upgrade
        {"method":"prebuilt","ref":"latest"}` against z4 correctly
        auto-resolves and re-installs the same gfx110X-tailored build with
        zero manual intervention; model verified working after.

## Documentation

- [x] 19. Memory: `z4-wedge-rootcause.md` (dedicated file, linked from
        `MEMORY.md`); corrected the stale one-liner for `z4-topology`.
- [x] 20. This openspec change.
- [x] 21. Update `docs-skein/deploy/llama-skein.md`: added a z4 section
        (was entirely absent), corrected the upgrade API's endpoint path
        (doc said `/api/skein/upgrade`, actually `/api/system/upgrade`), and
        documented the new ROCm/RDNA3-aware `prebuilt` behavior.

## Follow-ups (not done, explicitly out of scope here)

- [ ] 22. Consider the same lemonade-sdk swap for rocky (same gfx1100/RDNA3
        family) preventatively.
- [ ] 23. Consider whether llama-skein's tuning database should stop
        recommending `flash_attn: true` for gfx1100 by default.
