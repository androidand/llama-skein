# Tasks: Move `internal/server/api/skein.go` silence tests from unit tests to integration tests to verify real HTTP state.

# Detailed Tasks.md

## Phase 1 — Analysis & Discovery
- [ ] 1. Locate the existing unit tests for `internal/server/api/skein.go` that test silence logic.
  - Validation: `grep -r "silence" internal/server/api/ --include="*_test.go" -l`
- [ ] 2. Identify the current test runner configuration for unit tests to understand their setup and teardown.
  - Validation: `cat go.mod | grep -i test`
- [ ] 3. Locate the integration test directory structure to understand how integration tests are currently organized.
  - Validation: `find . -type d -name "*test*" -o -name "*integration*" | grep -v ".git"`
- [ ] 4. Identify the `Makefile` targets used to run unit tests versus integration tests.
  - Validation: `grep -A 2 "test" Makefile | head -20`

## Phase 2 — Unit Test Extraction
- [ ] 5. Extract the silence test functions (e.g., `TestSilence...`) from `internal/server/api/skein_test.go` and move them to a temporary staging file for manual review.
  - Validation: `grep -n "func Test.*Silence" internal/server/api/skein_test.go`
- [ ] 6. Remove the silence-related test functions from `internal/server/api/skein_test.go`.
  - Validation: `git diff internal/server/api/skein_test.go | head -50`
- [ ] 7. Update any imports or helper functions in `skein_test.go` that were used exclusively by the extracted silence tests.
  - Validation: `git status internal/server/api/skein_test.go`

## Phase 3 — Integration Test Creation
- [ ] 8. Create a new integration test file in the appropriate directory (e.g., `internal/server/api/integration/`) to house the silence logic.
  - Validation: `touch internal/server/api/integration/silence_test.go`
- [ ] 9. Implement the silence test suite in `internal/server/api/integration/silence_test.go`, rewriting the logic to use real HTTP clients (e.g., `http.Client` with `http.NewRequest` and `Do`) instead of mocked dependencies.
  - Validation: `grep -n "http.Get\|http.Post\|http.NewRequest" internal/server/api/integration/silence_test.go`
- [ ] 10. Add helper functions to `internal/server/api/integration/silence_test.go` to setup the test server context (if not already present in the integration test base).
  - Validation: `grep -n "Setup\|Start\|ListenAndServe" internal/server/api/integration/silence_test.go`
- [ ] 11. Configure the test server to handle the `/api/skein/silent` endpoint within the integration test logic.
  - Validation: `grep -n "/api/skein/silent" internal/server/api/integration/silence_test.go`

## Phase 4 — HTTP Verification
- [ ] 12. Verify the silence test returns a `200 OK` or the expected success state when the silent API is called.
  - Validation: `curl -X POST http://localhost:8080/api/skein/silent`
- [ ] 13. Verify the silence test checks for `503 Service Unavailable` when GPU power control is unavailable (as per recent commits).
  - Validation: `grep -n "503" internal/server/api/integration/silence_test.go`
- [ ] 14. Ensure the integration tests validate the actual HTTP status codes returned by the server instead of relying on internal function return values.
  - Validation: `grep -n "Status.*200\|Status.*503" internal/server/api/integration/silence_test.go`

## Phase 5 — CI/Build Updates
- [ ] 15. Update the repository's CI configuration (e.g., `.github/workflows/*.yml`) if necessary, to ensure integration tests run alongside unit tests.
  - Validation: `cat .github/workflows/test.yml | grep -i test`
- [ ] 16. Update the Makefile to ensure the new integration tests are included when running the `make test` or `make test-integration` target.
  - Validation: `grep -A 5 "test-integration" Makefile`
- [ ] 17. Run the updated test suite locally to verify no compilation errors occur and the silence logic is covered.
  - Validation: `make test-integration 2>&1 | head -30`

## Phase 6 — Documentation & Cleanup
- [ ] 18. Update the project README or architecture documentation to reflect that silence tests are now integration tests.
  - Validation: `grep -r "silence" README.md -i`
- [ ] 19. Verify the refactoring maintains the existing public API interface as per the scope.
  - Validation: `grep -r "func.*Silence" internal/server/api/`
- [ ] 20. Perform a final git diff review to ensure only the intended changes to test files were made.
  - Validation: `git diff --stat`
- [ ] 21. Submit the changes for code review and merge.
  - Validation: `git status`