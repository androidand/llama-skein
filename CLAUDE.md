# llama-skein — Claude compatibility notes

This file is intentionally minimal. Primary agent instructions live in `AGENTS.md`,
and the ecosystem workflow lives in the `skein-dev` skill.

@AGENTS.md

## Required reading order

1. `AGENTS.md` — project rules, testing, upstream discipline
2. The `skein-dev` skill — design-first contract flow, codegen, verification, deploy
3. `ECOSYSTEM.md` — where this repo sits and what the fork adds

## Private infra docs

Deploy details (host IPs, container IDs, service paths) are in the private companion
repo `~/dev/docs-skein`. Clone with
`git clone git@github.com:androidand/docs-skein.git ~/dev/docs-skein`.

## Why this file is short

It previously duplicated `AGENTS.md` and the workflow docs. Three copies of the same
rules drift, and the drift is what let agents skip the design-first flow — the spec
said one thing, `CLAUDE.md` another, and neither matched the code. Keep detailed
policy in `AGENTS.md` and the `skein-dev` skill; keep this a pointer.
