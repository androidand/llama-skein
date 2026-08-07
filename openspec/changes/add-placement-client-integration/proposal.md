# Proposal: Placement-aware clients (opencode + skein)

## Why

llama-skein now decides, per model, whether it runs full-GPU, hybrid
GPU+system-RAM, or CPU-only, and reports that decision on `/api/fit*` and
`/api/models`. The measured spread on z4 is not subtle: the same host serves
a full-GPU model at **70 tok/s** and a `cpu-bound-hybrid` model at
**0.81 tok/s** — roughly 90× — and both look equally "ready" to a client
that only reads model state.

Both consumers route work to local models today, and neither can see this:

- **opencode** picks a local provider/model per sub-agent task. Sending an
  interactive or latency-sensitive task to a cpu-bound-hybrid model is a
  minutes-long stall that reads as a hang.
- **skein** dispatches change-pipeline roles across the fleet and sweeps
  context sizes. It needs the same signal to avoid parking a coder role on a
  model that generates at conversational-typing speed.

The information exists; nothing consumes it. These were `add-auto-hybrid-
placement` tasks 18–19, deferred while the server side landed.

## What

### opencode (TypeScript)

- Regenerate `src/local/llama-skein/gen` from the updated OpenAPI spec, so
  the placement fields exist in the typed client.
- Teach local routing to read placement: a model whose effective placement is
  host-bandwidth-paced (`cpu-bound-hybrid` / `cpu-only`) is deprioritized for
  interactive work and only chosen when nothing better is reachable, or when
  the task is explicitly long-running/asynchronous.
- Surface the placement in whatever already shows local model status, so a
  slow response is legible as "this model is running from system RAM" rather
  than an unexplained stall.

### skein (Go)

- Pin `github.com/androidand/llama-skein` to the commit carrying the
  placement contract, so the generated `pkg/apicontract` types are available.
- Consume the placement in provider/model selection with the same rule as
  opencode: host-paced placements are a deliberate last resort, not a peer of
  a full-GPU model.
- The context-fit sweep must keep working against a hybrid model: its budget
  numbers now describe the GPU share, not the whole model.

## Non-goals

- No change to llama-skein itself; this change only consumes what it already
  publishes.
- No automatic *rejection* of hybrid models. A slow model is often the only
  one that can run a given large model at all — the point is informed
  ranking, not exclusion.
- No tok/s prediction. Placement reports a qualitative class; clients rank on
  that plus their existing measured signals.

## Sequencing

llama-skein's placement work is pushed on `skein/auto-hybrid-placement`
(a clean fast-forward from `main`). The two clients differ:

- **opencode does not need the merge.** Its generator takes a spec path, so
  the client can be generated from the branch's contract and committed
  independently.
- **skein does need it.** Its `go.mod` carries
  `replace github.com/androidand/llama-skein => ../llama-skein`, so local
  builds always resolve to that checkout regardless of the `require` version
  — bumping the pin alone changes nothing. skein's placement code therefore
  compiles only once the branch lands on `main` in `~/dev/llama-skein` (or,
  for development, while the replace points at the worktree).

So skein's side is written and validated against the worktree, and is a
one-line pin update away from building normally after the merge. This
matches the repo rule that llama-skein is pushed before skein when a change
spans both.

## Risks

- **Two repos, one contract.** Mitigated by generating opencode's client from
  the spec rather than hand-writing types, and by consuming skein's side
  through the generated `pkg/apicontract`.
- **Over-avoidance.** A rule that refuses hybrid models entirely would make
  large models unusable. The ranking must degrade preference, not
  availability.
