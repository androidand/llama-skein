# Spec delta: engine-upgrade-targeting

## ADDED Requirements

### Requirement: The managed engine path is declared, not discovered

Configuration SHALL provide a top-level `enginePath` key naming the `llama-server`
binary that the upgrade API manages, and when it is set the upgrade API SHALL install
to exactly that path without inspecting running processes.

#### Scenario: Explicit path is honoured

- **WHEN** `enginePath` is set and an upgrade is requested
- **THEN** the new binary is installed at that path
- **AND** no process list is consulted to choose the destination

#### Scenario: A second engine build is untouched

- **WHEN** a host runs both the managed engine and a separately-built engine, and an
  upgrade is requested
- **THEN** only the managed engine's binary and directory are written
- **AND** the other build's binary and shared libraries are byte-identical afterwards

### Requirement: Ambiguous discovery fails instead of guessing

When no engine path is configured, the upgrade API SHALL consider only running
processes whose binary basename is exactly `llama-server`, and SHALL refuse the
request when the surviving candidates do not agree on a single path.

#### Scenario: A differently-named engine is not a candidate

- **WHEN** the only running engine's binary basename is not exactly `llama-server`
- **THEN** it is not selected as the install destination

#### Scenario: Conflicting candidates

- **WHEN** two running processes named `llama-server` resolve to different paths
- **THEN** the upgrade fails with an error listing the conflicting paths and naming
  the configuration key that resolves the ambiguity
- **AND** nothing on disk is modified

#### Scenario: Nothing running and nothing configured

- **WHEN** no candidate process exists and no engine path is configured
- **THEN** the existing default installation path is used if present, otherwise the
  request fails with an error

### Requirement: The restart sweep only stops the managed engine

Restarting the engine after an upgrade SHALL terminate only the managed engine
process, identified by the same rule used to choose the install destination.

#### Scenario: Unrelated engine survives a restart

- **WHEN** an upgrade completes on a host that is also running a differently-named
  engine process
- **THEN** that process is still running afterwards

### Requirement: A rollback restores a consistent engine

The upgrade API SHALL NOT write shared libraries into a directory it was not directed
at, because an upgrade replaces those libraries alongside the binary and a
binary-only backup cannot undo it.

#### Scenario: Libraries follow the declared path

- **WHEN** shared libraries are installed as part of an upgrade
- **THEN** they are written only into the directory containing the declared or
  unambiguously-discovered managed engine
