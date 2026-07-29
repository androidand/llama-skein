# Tasks: Add persistent user profile saving to `/api/skein/config` to avoid manual DPM/APU tuning.

# Implementation Plan: Persistent User Profile Saving to `/api/skein/config`

## Phase 1 — Analysis & Design
- [x] 1. Analyze existing `/api/skein/config` endpoint implementation to understand current behavior and data structures.
  - Validation: `git log --all --oneline --graph --decorate -20`
- [x] 2. Review existing config file structure and storage mechanisms used elsewhere in the repository (e.g., `/api/config`, thermal management).
  - Validation: `git grep -l "config\." -- "*.go"`
- [x] 3. Design the JSON payload schema for user profiles, defining fields for DPM/APU settings, silent mode preferences, and optional metadata.
  - Validation: `cat /dev/null`

## Phase 2 — Backend Implementation: Profile Model
- [x] 4. Create the `UserProfile` struct in the internal/server/models directory to represent the profile data.
  - Validation: `git diff HEAD --stat internal/server/models/`
- [x] 5. Implement the serialization logic (ToJSON/FromJSON) in the models package to handle complex nested settings (DPM/APU).
  - Validation: `go test ./internal/server/models/... -run TestUserProfile`
- [x] 6. Create the repository pattern (e.g., `UserProfileRepository`) to handle file I/O operations for saving and loading profiles.
  - Validation: `git diff HEAD --stat internal/server/repo/`

## Phase 3 — Backend Implementation: API Endpoint
- [x] 7. Implement the `POST /api/skein/config` endpoint handler to accept the profile payload, validate input constraints, and trigger the save operation.
  - Validation: `git diff HEAD --stat internal/server/routes/`
- [x] 8. Implement the `GET /api/skein/config` endpoint handler to retrieve the currently active profile or a default template.
  - Validation: `git diff HEAD --stat internal/server/routes/`
- [x] 9. Integrate profile validation logic to ensure DPM/APU ranges are within hardware limits before saving.
  - Validation: `go vet ./internal/server/...`

## Phase 4 — Backend Implementation: Service Layer
- [x] 10. Create the `ConfigService` interface and implementation to encapsulate business logic for switching profiles and applying settings.
  - Validation: `git diff HEAD --stat internal/server/service/`
- [x] 11. Wire up the service to the existing hardware control layer (e.g., thermal/power management) to apply settings when a profile is loaded.
  - Validation: `go test ./internal/server/service/... -run TestConfigService`

## Phase 5 — Testing
- [x] 12. Write unit tests for the `UserProfileRepository` to cover success, failure (disk full), and edge cases (invalid JSON).
  - Validation: `go test ./internal/server/repo/... -run TestUserProfileRepository`
- [x] 13. Write integration tests for the `/api/skein/config` endpoints to verify correct HTTP status codes and response payloads.
  - Validation: `go test ./internal/server/routes/... -run TestSkeinConfigRoute`
- [x] 14. Verify the test suite passes on the AMD GPU target (`am17an` fork) if applicable to the environment.
  - Validation: `go test ./...`

## Phase 6 — Documentation & Polish
- [x] 15. Update the `CLAUDE.md` to reflect the new API endpoint in the routing documentation.
  - Validation: `git diff HEAD --stat CLAUDE.md`
- [x] 16. Update the public API documentation (if any exists in a `docs/` or `README.md` file) to describe the new profile structure.
  - Validation: `git diff HEAD --stat README.md docs/`
- [x] 17. Ensure all new Go code adheres to the project's formatting and linting standards.
  - Validation: `gofmt -l .`