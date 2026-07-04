# Spec delta: public-launch-readiness

## ADDED Requirements

### Requirement: Fork identity in public-facing docs

The repository README SHALL present llama-skein's own identity: a fork of
mostlygeek/llama-swap with an explicit divergence list, install instructions
that build THIS fork, and badges/links that reference
`github.com/androidand/llama-skein`. Upstream install channels (Docker images,
Homebrew tap, WinGet) SHALL be labelled as upstream's, since they do not
contain fork features.

#### Scenario: Stranger lands on the repo

- **WHEN** a visitor with no skein context opens the repository front page
- **THEN** the README states what llama-skein adds over llama-swap and links
  working install instructions for the fork itself
- **AND** no badge or install command silently resolves to
  mostlygeek/llama-swap artifacts

### Requirement: CI targets the fork

Scheduled and release workflows SHALL operate on this repository's actual
default branch (`main`) and SHALL NOT push to registries or repositories the
fork does not own; upstream-only jobs SHALL be guarded by a repository check.

#### Scenario: Weekly upstream sync runs

- **WHEN** the `sync-upstream` scheduled workflow triggers
- **THEN** it checks out and rebases `main` (not a deleted feature branch)
- **AND** pushes only to `origin` with force-with-lease

#### Scenario: Docker workflow runs on the fork

- **WHEN** `unified-docker.yml` is evaluated on androidand/llama-skein
- **THEN** it either targets a fork-owned registry or skips via a
  `github.repository` guard, and never attempts `ghcr.io/mostlygeek/*`

### Requirement: No personal infrastructure in tracked files

Tracked files SHALL NOT contain the owner's machine-specific absolute paths,
private fleet topology, or the private companion docs repo. The `docs-skein/`
directory SHALL be gitignored so the private repo cannot be committed from
inside this working tree.

#### Scenario: Presentability grep after scrub

- **WHEN** `git grep '/Users/andreas'` and a personal-topology grep run over
  tracked docs and scripts (excluding git history and archived openspec
  changes)
- **THEN** no hits remain outside content the owner explicitly chose to keep
- **AND** `git check-ignore docs-skein` succeeds
