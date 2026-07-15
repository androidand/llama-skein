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

## Fleet rollout (same day, follow-up to the above)

- [x] 22. Rolled out to rocky (gfx1100/RDNA3) and proxmox (gfx1201/RDNA4) via
        the now ROCm-aware upgrade API — each auto-resolved its own
        lemonade-sdk bucket (`gfx110X`/`gfx120X`) with zero manual flags.
- [x] 23. Found + fixed a real bug live during the rocky rollout:
        `resolvePrebuiltSource` was gated on `detectROCm()` (checks for the
        `hipcc` dev-toolchain compiler) — wrong for `prebuilt`, which needs
        no local toolchain at all. Rocky runs ROCm inference fine via
        runtime libs borrowed from Ollama's bundle with no `hipcc` anywhere
        on the host, so detection silently failed and fell through to a CPU
        build (caught by the smoke test, rolled back cleanly, no harm done).
        Fixed (commit `fa0ff30`): resolve on the detected GPU arch
        (`s.tunedGfx`, sysfs-only) instead. Removed the now-fully-unused
        `detectROCm()`.
- [x] 24. Rocky fully validated: clean upgrade, model verified working,
        98%/68% GPU-util/mem-activity during real generation (healthy,
        matches z4's pattern).
- [x] 25. Proxmox: upgrade succeeded cleanly, currently-loaded MTP model
        verified working. **Open item**: one genuine backend crash observed
        (EOF/connection-refused, self-recovered via llama-skein's existing
        health-check/reload) while testing concurrently with the user's own
        live traffic. No kernel-level GPU fault in dmesg (same clean pattern
        as z4). Not yet isolated whether this is build-specific, an
        MTP+concurrency interaction, or unrelated — user chose to keep the
        new build and watch rather than roll back or force more test load
        onto a host they were actively using. See z4-wedge-rootcause memory
        for full detail.

## Follow-ups (not done, explicitly out of scope here)

- [ ] 26. Watch proxmox for a recurrence of the crash above; `.prev` backup
        is in place if a rollback is needed.
- [ ] 27. Consider whether llama-skein's tuning database should stop
        recommending `flash_attn: true` for gfx1100 by default.
