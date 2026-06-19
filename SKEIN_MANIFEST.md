# skein.json — ecosystem manifest

This repo carries a `skein.json` at its root. It is a small, machine-readable
description of **what this project is and what it does**, used by the skein
ecosystem page at <https://tantonet.se/skein>.

## What it does

The website (`my-astro-site`) fetches the `skein.json` from each ecosystem repo
at **build time** and renders the ecosystem page from them:

```
opencode-skein/skein.json  ─┐
llama-skein/skein.json      ─┼─→  my-astro-site build  →  tantonet.se/skein
skein/skein.json            ─┘
```

The **source of truth lives with the code**. When this project gains a feature,
you add it to *this* `skein.json` — not to the website — and the page reflects it
on the next site build. No drift, no separate marketing copy to keep in sync.

## How to use / update it

1. Edit `skein.json` here. Add or update entries in the `features` array.
2. Each feature needs at least `id`, `title`, `summary`, `status`
   (`experimental` | `beta` | `stable`).
   - `summary` is plain-English and user-facing — it's what the page shows.
   - `upstreamable` (`yes` | `partial` | `no` | `na`) is optional; for forks it
     records whether the feature could be proposed upstream.
   - `depends` lists other skein ids a feature relies on (e.g. `llama-skein`).
3. Bump `updated` to today's date.
4. Validate against the shared schema:
   [`skein.schema.json`](https://raw.githubusercontent.com/androidand/opencode/dev/skein.schema.json)
   (referenced by the `$schema` field — editors with JSON Schema support will
   autocomplete and validate).
5. Commit and push to the branch the site reads (the repo's default branch).

## Schema

The canonical schema is maintained once, in the `opencode-skein` repo:
<https://raw.githubusercontent.com/androidand/opencode/dev/skein.schema.json>.
All ecosystem repos point their `$schema` at it so the shape stays consistent.
