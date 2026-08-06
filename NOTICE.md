# Third-party notices

llama-skein is MIT licensed (see `LICENSE.md`). It also contains code derived
from the third-party projects below. This file records that ancestry so the
obligations travel with the code (skein `fleet-model-gallery` task 2.5).

## llmfit

- **Upstream:** https://github.com/AlexsJones/llmfit
- **License:** MIT
- **Upstream license text:** https://github.com/AlexsJones/llmfit/blob/main/LICENSE

MIT requires that the upstream copyright and permission notice travel with any
substantial portion of the work. The derived code below is substantial, so that
notice belongs here.

> **Action required before this repository is published.** The verbatim
> upstream copyright line ("Copyright (c) <year> <holder>") is not reproduced
> here, because it was not copied at adoption time and inventing it would be
> worse than leaving a visible gap. Copy it from the upstream `LICENSE` into
> this section before making the repository public.

### Derived code

| Here | Upstream | Upstream commit |
| --- | --- | --- |
| `internal/fit/` — model footprint, max-safe-context, and fit verdict | `llmfit-core/src/{fit,hardware,models}.rs` | not recorded at adoption |
| `internal/operation/shard.go` — `ParseShardInfo`, `GroupShards` | `llmfit-core/src/providers.rs` (`parse_shard_info`, `build_gguf_candidates`) | `850e80900a583ebb07f8efeab07589dcfd444d92` |
| `internal/perf/monitor_darwin.go` — Apple unified-memory availability | `llmfit-core/src/hardware.rs` | not recorded at adoption |
| Fit score components in the API contract | `llmfit-core/src/fit.rs` (`ScoreComponents`) | not recorded at adoption |

Only the shard port pinned its upstream commit. The others cite the file but
not the revision, which means a future reader cannot tell what upstream state
they match or whether upstream has since diverged. Pin them when each is next
touched; `internal/operation/shard.go`'s header comment is the pattern to
follow.

Behaviour that is llama-skein's own and has no llmfit equivalent is marked as
such at each site — for example `ShardSetComplete`, which exists because
llama-skein validates a declared artifact set before download where llmfit
scans an already-complete directory.

### Related adoption records

The same upstream is adopted independently in the sibling repository
`opencode-skein`, which keeps a machine-readable manifest at
`packages/opencode/src/local/model-catalog/adoption-manifest.ts`
(llmfit quantization tables, pinned at commit
`12c0edb74b34ad867047c084e5595d3841a08163`). The two adoptions are separate
ports of different parts of llmfit; neither supersedes the other.
