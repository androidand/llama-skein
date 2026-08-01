# Proposal: Preempt, don't fail, when a model switch waits on a busy model

## Why

A user switched models while their previous session had a stuck in-flight
generation on the loaded model (2026-08-01). llama-skein waited for the busy
model to drain, timed out, and failed the switch with "Timed out waiting for a
busy model to become available" — the user's own runaway request held their
fleet hostage, and the error named neither the culprit nor a way out.

The swap-wait default protects in-flight work, which is right for a shared
server but wrong as a dead end: an explicit model-switch request is a human
decision and must eventually win.

## What changes

- When a swap blocks on a busy model, wait `swapDrainSecs` (default: current
  timeout), then **preempt**: cancel the in-flight request(s) via the existing
  slot-cancel path, unload, and proceed with the switch. The cancelled
  request's client receives a clear error ("preempted by model switch").
- The interim 503 (while waiting) names the blocking model, its in-flight
  count, and the age of the oldest request — so a client can show "waiting on
  <model>, request running for 4m" instead of a mystery retry loop.
- `swapPolicy: wait` opts back into fail-on-timeout for deployments that
  prefer it. Warn-not-enforce applies to us telling the user, not the user
  telling the system.

## Non-goals

- No change to slot-cancel-on-disconnect (already works).
- No priority system between models; requests are preempted oldest-first.
