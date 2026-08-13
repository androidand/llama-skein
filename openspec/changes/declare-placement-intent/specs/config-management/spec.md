# A model declares its placement constraints

## ADDED Requirements

### Requirement: A model may declare placement intent instead of pinning flags

`ModelConfig` SHALL accept an optional `placement:` block expressing the
constraints the operator cares about, leaving llama-skein to choose the flags that
satisfy them.

The block SHALL support a free-text `reason`, surfaced on the fit report. A
declaration without a reason is still a declaration, but the reason is what makes
the entry reviewable after the person who wrote it has moved on.

`reason` SHALL be the block's only field in the first increment. A vocabulary such as
`intent` SHALL NOT ship before the behaviour that gives its values meaning: an enum
accepted but not acted on is read as documentation, and acquires behaviour later
against configs written when it had none. `reason` and the `declared` flag are
immune to this because both are descriptive — they report what the config says, and
promise nothing about what the planner will do.

An unrecognised key inside the block SHALL be rejected rather than ignored, so a
forward-looking value cannot be written under one meaning and reinterpreted under
another.

Absence of the block SHALL mean `auto` — identical to today's behaviour for a model
with no placement flags — so every existing config is unaffected.

The block SHALL be additive: no existing field changes meaning, and a config that
does not use it behaves exactly as before.

#### Scenario: Model declares a context constraint

- **WHEN** a model declares `intent: context` with `minContext: 200000` and a reason
- **THEN** the constraint is available to the planner and the reason appears on the
  fit report

#### Scenario: Existing config without the block

- **WHEN** a model has no `placement:` block
- **THEN** it is planned exactly as it is today, and nothing in its behaviour changes

#### Scenario: Declaration without a reason

- **WHEN** a `placement:` block omits `reason`
- **THEN** the placement is still declared, and the missing reason is not an error

#### Scenario: Forward-looking key written before its behaviour exists

- **WHEN** a config sets a placement key the current increment does not implement
- **THEN** it is rejected, rather than accepted and silently reinterpreted once the
  behaviour lands

### Requirement: A declaration and a raw flag together are reported, not silently merged

A config warning SHALL be emitted when a model carries both a `placement:` block
and a raw placement flag in its `cmd`.

The raw flag SHALL win, preserving the existing "explicit flags always win"
contract that upstream llama.cpp also follows per-argument. The conflict SHALL be
reported rather than resolved in silence.

Silently honouring one of two contradictory instructions is the precise failure this
change exists to end, so the conflict SHALL NOT be invisible even though it is
non-fatal.

#### Scenario: Block and flag disagree

- **WHEN** a model declares `intent: latency` but its `cmd` pins `--n-gpu-layers 40`
- **THEN** the pinned flag is honoured, and a config warning names the conflict and
  the model

#### Scenario: Block with no conflicting flag

- **WHEN** a model declares intent and pins no placement flag
- **THEN** no conflict warning is emitted

### Requirement: Removing a declared placement does not require rewriting the command

Placement declarations SHALL be removable by deleting a field, without a
read-modify-write cycle over the `cmd` string.

Removing a raw pinned flag today requires patching the whole `cmd`, because the
patch contract can only overwrite `n_gpu_layers` and never remove it. That route is
also unsafe: `GET /api/models/config/{id}` returns `--port` already resolved, so a
round-trip hardcodes a dynamically allocated port. A structured block avoids both
problems, and supersedes the removal path tracked on
`flag-under-offloaded-models`.

#### Scenario: Operator clears a declaration

- **WHEN** a placement declaration is removed
- **THEN** the model returns to `auto` planning and its `cmd` string is not touched

#### Scenario: Port placeholder is never at risk

- **WHEN** placement is changed through the declaration rather than the command
- **THEN** `${PORT}` in the stored `cmd` is untouched, because the command is never
  rewritten
