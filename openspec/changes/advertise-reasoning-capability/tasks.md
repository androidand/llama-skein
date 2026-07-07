# Tasks: advertise-reasoning-capability

- [ ] 1. Spec: add `reasoning` boolean to the `Model` schema in
       `contracts/llama-skein.openapi.json`; `go generate ./pkg/apicontract`
       + gofmt.
       Validation: `make check-codegen`

- [ ] 2. Config: add `Reasoning *bool` (`yaml:"reasoning"`) to
       `internal/config/model_config.go`.
       Validation: `go build ./internal/config/`

- [ ] 3. Handler: in `internal/server/api.go` newRecord, set
       `rec.Reasoning = mc.Reasoning`. Test in `internal/server/` that a model
       with `reasoning: true` surfaces `reasoning:true` in `/v1/models` and a
       model without it omits the field.
       Validation: `go test ./internal/server/ -run 'Reasoning|ListModels'`

- [ ] 4. Repo validation: gofmt, `go build ./...`, `go test -short ./...`,
       `make check-codegen`.

- [ ] 5. (companion, opencode repo `map-reasoning-capability`) regen the TS
       client and map `item.reasoning` → `capabilities.reasoning` in
       discoverOpenAICompatibleModels.
