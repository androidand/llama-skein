# Tasks: add-model-size

- [x] 1. Spec: add `size_bytes` (integer int64) to the `Model` schema in
       `contracts/llama-skein.openapi.json`; `go generate` + gofmt.
- [x] 2. `computeModelSizeBytes` (GGUF stat / MLX safetensors sum) +
       `modelSizeBytes` (memoized per id) in `internal/server`.
- [x] 3. Set `rec.SizeBytes` in the `/v1/models` record builder.
- [x] 4. Test `computeModelSizeBytes` (GGUF file, missing path, mlx/vllm → 0).
- [x] 5. `go build`, `go test -short ./proxy/... ./internal/...`,
       `make check-codegen`.
- [ ] 6. opencode-skein: regen client; show size in the model picker.
- [ ] 7. skein: show size in the model / provider-models listing.
- [ ] 8. Deploy llama-skein so `/v1/models` returns `size_bytes` fleet-wide.
