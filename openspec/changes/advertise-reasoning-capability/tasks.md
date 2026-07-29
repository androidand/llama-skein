# Tasks: advertise-reasoning-capability

- [x] 1. Spec: add `reasoning` boolean to the `Model` schema in
       `contracts/llama-skein.openapi.json`; `go generate ./pkg/apicontract`
       + gofmt.
       Validation: `make check-codegen`

- [x] 2. Config: add `Reasoning *bool` (`yaml:"reasoning"`) to
       `internal/config/model_config.go`.
       Validation: `go build ./internal/config/`

- [x] 3. Handler: in `internal/server/api.go` newRecord, set
       `rec.Reasoning = mc.Reasoning`. Test in `internal/server/` that a model
       with `reasoning: true` surfaces `reasoning:true` in `/v1/models` and a
       model without it omits the field.
       Validation: `go test ./internal/server/ -run 'Reasoning|ListModels'`

- [x] 4. Repo validation: gofmt, `go build ./...`, `go test -short ./...`,
       `make check-codegen`.

<!-- DEFERRED 2026-07-26: Requires opencode repo. -->

- [ ] 5. (companion, opencode repo `map-reasoning-capability`) regen the TS
        client and map `item.reasoning` → `capabilities.reasoning` in
        discoverOpenAICompatibleModels.
