# OpenAPI Contract Workflow

This content now lives in the **`skein-dev` skill**, at
`.claude/skills/skein-dev/references/openapi-contract.md` (symlinked into
`~/.claude/skills/skein-dev`, so it loads from any repo in the ecosystem).

`contracts/llama-skein.openapi.json` remains the source of truth for the
llama-skein control API: **edit the spec before any handler or client.**

The skill covers the ordered procedure, the Go and TypeScript generation
commands, what to do when a route is renamed or moved, where generated types
must be used in each repo, the cross-repo drift check, and how to confirm the
spec matches what a live provider actually serves.

## Why this is a pointer

This document, `CLAUDE.md`, and `AGENTS.md` each carried their own copy of the
workflow, and they drifted. This file claimed skein resolves llama-skein through
a `replace` directive; `CLAUDE.md` claimed there was no replace and that
`GOWORK=off` fetched a pinned version from GitHub. Only one could be right —
`skein/go.mod:84` does carry the replace — and an agent following the wrong copy
would draw the wrong conclusion about whether a change had been verified.

One copy, in the skill.
