# Proposal: Move `internal/server/api/skein.go` silence tests from unit tests to integration tests to verify real HTTP state.

## Context
No additional context provided.

## Why
This idea was submitted for autonomous implementation via the Ralph loop.

## What
Move `internal/server/api/skein.go` silence tests from unit tests to integration tests to verify real HTTP state.

## Scope
- Design and implement the core change
- Add tests covering the new behaviour
- Update documentation if the public interface changes

## Risks
- Scope creep — mitigated by breaking work into small, verifiable tasks
- Unclear requirements — mitigated by the ANALYSIS phase before coding
