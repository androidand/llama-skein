# Tasks: Add unit tests for silent mode 503 fallback in internal/server/silent to prevent regressions.

- [ ] 1. Analyze `internal/server/silent` implementation to understand the 503 fallback logic and dependencies.
  - Validation: `ls internal/server/silent/` and `grep -r "503" internal/server/silent/`

- [ ] 2. Identify or create mock interfaces for GPU power control/state dependencies used by the silent mode handler.
  - Validation: `grep -r "interface" internal/server/silent/ || echo "No interfaces found, checking struct dependencies"`

- [ ] 3. Write unit tests for the positive case: Silent mode returns success when GPU power control is available and functional.
  - Validation: `go test ./internal/server/silent/... -run TestSilentModeSuccess -v`

- [ ] 4. Write unit tests for the negative case: Silent mode returns 503 when GPU power control is unavailable or errors.
  - Validation: `go test ./internal/server/silent/... -run TestSilentMode503Fallback -v`

- [ ] 5. Write unit tests for edge cases: Silent mode returns 503 if the GPU state probe fails intermittently.
  - Validation: `go test ./internal/server/silent/... -run TestSilentModeEdgeCases -v`

- [ ] 6. Run the full test suite for the `internal/server/silent` package to ensure no regressions.
  - Validation: `go test ./internal/server/silent/... -cover`

- [ ] 7. Verify that the new tests pass locally and that coverage has increased for the 503 fallback logic.
  - Validation: `go test ./internal/server/silent/... -v -coverprofile=coverage.out && go tool cover -func=coverage.out | grep silent`