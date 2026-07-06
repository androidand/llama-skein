# Tasks: add-gpu-tuning-profiles

- [x] 1. GPU detection (`internal/tuning/detect.go` + `detect_test.go`):
       `DetectGfx(sysfsRoot string) (gfx string, deviceID uint32, ok bool)`
       reading `/sys/class/drm/card*/device/{vendor,device}`, AMD device-ID→gfx
       map (D1). Test with a fake sysfs tree fixture for each known ID + an
       unknown ID (→ ok=false).
       Validation: `go test ./internal/tuning/ -run TestDetectGfx`

- [x] 2. Tuning database + loader (`internal/tuning/profiles.yaml` embedded via
       `//go:embed`, `internal/tuning/profiles.go` + `profiles_test.go`):
       `Profile`/`MTPProfile`/`UseCase` structs; seed YAML with gfx1201
       (verified) + gfx1100/gfx1030 (conservative) for use-case
       `agentic-single`, each with notes + sources (D2). `LoadProfiles()`
       parses embedded + optional user file (`<configdir>/tuning-profiles.yaml`)
       merged over it; `Lookup(gfx, usecase) (Profile, bool)`. Test: embedded
       parses; gfx1201 verified w/ MTP; user file overrides an entry + adds a
       new gfx.
       Validation: `go test ./internal/tuning/ -run 'TestLoadProfiles|TestLookup'`

- [x] 2a. Injector (`internal/tuning/apply.go` + `apply_test.go`): pure
       `ApplyProfile(cmd string, eff Profile, isMTP bool) string` appending
       only missing flags (alias-aware: --flash-attn/-fa, --parallel/-np,
       --spec-type). Test: explicit --parallel 4 preserved; fa injected when
       absent; MTP flags only when isMTP; extra_args appended; idempotent.
       Validation: `go test ./internal/tuning/ -run TestApplyProfile`

- [x] 2b. Override resolution (`internal/tuning/override.go` +
       `override_test.go`): the `Override` struct (pointer fields + ExtraArgs
       + Enabled + GfxTarget per design D6) and
       `Resolve(profile Profile, ovr Override) (effective Profile, enabled bool)`.
       Test: Enabled=false → injects nothing; FlashAttn=&false forces off on
       gfx1201; nil field defers to profile; ExtraArgs appended; provenance
       (recommended vs override) computed per field.
       Validation: `go test ./internal/tuning/ -run TestResolve`

- [x] 3. MTP-capability helper (`internal/tuning/mtp.go` or in profiles.go):
       `IsMTPModel(mc config.ModelConfig) bool` using metadata.mtp.enabled
       with GGUF-name fallback (D5) + test.
       Validation: `go test ./internal/tuning/ -run TestIsMTP`

- [x] 4. Spec: add `TuningProfile`, `TuningStatus` (with enabled + per-value
       recommended/override provenance), `TuningPatchRequest` (enabled,
       flash_attn, parallel, mtp, extra_args, gfx_target — nullable to reset)
       schemas and `/api/tuning`, `/api/tuning/profiles` paths + the
       `ConfigModelDetail.effective_flags` field to
       `contracts/llama-skein.openapi.json`; `go generate ./pkg/apicontract`
       + gofmt.
       Validation: `make check-codegen`

- [x] 5. Wire detection + profile into the Server (`internal/server/server.go`
       resolve gfx/profile on New(); honor `tuning.gfxTarget` /
       `tuning` override from config). Add `tuning` config block to
       `internal/config`. Unit test that override beats detection.
       Validation: `go test ./internal/server/ -run Tuning`

- [x] 6. Apply injection at launch: call `tuning.ApplyProfile` where the
       llamacpp command is materialized for a model (locate via the fit
       path's `SanitizedCommand` caller / process launch). Guard to
       backend==llamacpp. Test that a launched gfx1201 MTP model gains the
       verified flags and a non-MTP one does not.
       Validation: `go test ./internal/server/ -run TuningInject`

- [x] 7. Tuning handlers (`internal/server/apituning.go` + test):
       GET /api/tuning, GET /api/tuning/profiles, PATCH /api/tuning
       (persist override to config, trigger reload). Populate
       `effective_flags` in the model detail handler.
       Validation: `go test ./internal/server/ -run TestServer_Tuning`

- [x] 8. Docker default config comment + `config.docker-default.yaml`: note
       that flash-attn/parallel are now auto-injected per detected GPU and the
       explicit `--parallel 1` there is redundant-but-harmless (keep for
       clarity on CPU/unknown hosts where no profile applies).
       Validation: `grep -A8 cmd config.docker-default.yaml`

- [x] 9. Repo validation: gofmt, `go build ./...`, `go test -short ./...`,
       `make check-codegen`.
