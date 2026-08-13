# Make the recommended remedy expressible, and safe to round-trip

Both requirements below were found while applying this change's own recommended fix
to host A on 2026-08-12. The remedy the fit report should advise — remove the pin —
could not be expressed through the config API, and the obvious client workaround
silently corrupts the model command.

## ADDED Requirements

### Requirement: A placement pin can be removed, not only overwritten

`ConfigModelPatchRequest` SHALL provide a way to *remove* `--n-gpu-layers` from a
model's command, distinct from setting it to a value.

`patchModelInConfig` currently maps `n_gpu_layers` straight into the flag map
(`internal/server/apiconfig.go:793-798`), so every value writes the flag and none
removes it. There is no removal path, and `0` is actively dangerous: it writes
`--n-gpu-layers 0`, placing every layer on the CPU — the opposite of the intent, and
worse than the misconfiguration being corrected.

This blocks this change's own advice. Telling an operator that a pin should be
removed, through an API that can only overwrite it, leaves the planner disabled and
the operator maintaining a constant. It also blocks opencode-skein
`per-model-placement-controls`, whose primary action is clearing the pin.

The removal semantics SHALL follow the convention the contract already uses for
companion paths (`mmproj_path`, `draft_model_path`), where the empty value removes
the flag, rather than inventing a third pattern. Whatever encoding is chosen, `0`
SHALL NOT silently mean "remove".

#### Scenario: Operator removes a placement pin

- **WHEN** a patch requests removal of `n_gpu_layers`
- **THEN** `--n-gpu-layers` is deleted from the model's `cmd`, and placement is
  computed by the planner on the next load

#### Scenario: Zero is not a removal

- **WHEN** a patch sets `n_gpu_layers` to `0`
- **THEN** it either writes `--n-gpu-layers 0` as an explicit and deliberate
  all-CPU placement, or is rejected — but is never treated as removal

#### Scenario: Removing a pin that is not present

- **WHEN** removal is requested for a model whose `cmd` has no `--n-gpu-layers`
- **THEN** the patch is a no-op: the config is not rewritten and no model reloads

### Requirement: A model command round-trips without losing its placeholders

`GET /api/models/config/{id}` SHALL return the model command as **stored**, with
`${PORT}` and any other placeholder intact, so that a read-modify-write cycle
through the API preserves it.

The endpoint currently returns the command with `${PORT}` already resolved to the
port of the running instance — observed on host A as `--port 5803` against a stored
`--port ${PORT}`. Any client that reads a command, edits it, and patches it back
therefore hardcodes a port that llama-swap allocates dynamically, breaking that
model on a future launch. The corruption is silent and survives until the port
collides.

This is the read-modify-write cycle opencode-skein
`per-model-placement-controls` is built on, and the only cycle available for
editing a flag the typed fields do not cover.

Where a resolved command is genuinely useful, it SHALL be exposed as a separate,
clearly named field rather than in place of the stored one — the existing
`effective_flags` field is the precedent.

#### Scenario: Read-modify-write preserves the port placeholder

- **WHEN** a client reads a model's `cmd`, removes one flag, and patches the result back
- **THEN** the stored command still contains `${PORT}` and the model still receives a
  dynamically allocated port

#### Scenario: Resolved command is still available

- **WHEN** a caller needs the command as actually launched
- **THEN** it reads a distinctly named field, and the stored `cmd` remains
  unsubstituted
