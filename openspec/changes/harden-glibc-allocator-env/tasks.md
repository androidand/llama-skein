# Tasks: harden-glibc-allocator-env

- [ ] 1. Spec: in `contracts/llama-skein.openapi.json` add `backend_env`
       (object, additionalProperties string) to `TuningStatus` and
       `backend_env` (boolean, nullable) to `TuningPatchRequest`.
       `go generate ./pkg/apicontract` + gofmt.
       Validation: `make check-codegen`

- [ ] 2. Database: add a top-level `backend_env.linux_glibc` map to
       `internal/tuning/profiles.yaml` (the two `MALLOC_*` vars = "65536" with
       rationale + source), and a `BackendEnv` field to the `Database` struct
       (`profiles.go`); merge user-file overrides in `merge()`.
       Validation: `go test ./internal/tuning/ -run Profiles`

- [ ] 3. Config/override: add `BackendEnv *bool` (`yaml:"backend_env"`) to
       `config.TuningConfig` and to `tuning.Override`; map it in `ToOverride`.
       Validation: `go build ./internal/config/ ./internal/tuning/`

- [ ] 4. Inject: add `Database.BackendEnvFor(tc)` returning the effective env
       map (nil on non-Linux or when disabled), and extend `ApplyToConfig` to
       inject each var into every llamacpp model's `Env` unless the key is
       already present. Respect `tuning.enabled:false` (skip all) and
       `tuning.backend_env:false` (skip env only).
       Validation: `go test ./internal/tuning/ -run 'Env|ApplyToConfig'`

- [ ] 5. API: surface `backend_env` in `buildTuningStatus`; handle
       `backend_env` in `patchToConfig` + `writeTuningToConfig`.
       Validation: `go test ./internal/server/ -run Tuning`

- [ ] 6. Entrypoint: export the two `MALLOC_*` vars in
       `docker-entrypoint.sh` before `exec llama-skein`.

- [ ] 7. Repo validation: gofmt, `go build ./...`, `make test-dev`,
       `make check-codegen`.

- [ ] 8. (companion, opencode repo) regen the TS client
       (`bun run build:llama-skein-client`) so `TuningStatus.backend_env` /
       `TuningPatchRequest.backend_env` are in sync. No UI wiring required.

- [ ] 9. Deploy: roll the new binary + entrypoint to the Linux GPU hosts
       (proxmox, z4, rocky when online); verify `GET /api/tuning` reports
       `backend_env` and the running `llama-server` has the vars in its env.
