# Model capabilities: listing, reasoning, tools, chat templates

What each per-model knob actually does, and which ones are advertisements versus
behaviour. Written after a bring-up where every one of these caused a wrong
conclusion about a model.

## `unlisted` — hides from `/v1/models`, deletes nothing

```yaml
models:
  some-model:
    unlisted: true
```

Removes the model from `GET /v1/models`. It stays fully configured, loadable, and
reachable by its exact ID — `POST /v1/chat/completions` with `"model": "some-model"`
works normally.

**It looks like deletion from a client.** Anything that populates a picker from
`/v1/models` — opencode-skein's model selector included — will show the model
vanishing with no explanation. Say so when you set it, or someone will reasonably
conclude the weights are gone.

Use it for models that must not be picked by accident: research-licensed weights,
half-validated builds, benchmark-only entries.

## `reasoning` — an advertisement, not behaviour

```yaml
    reasoning: true
```

Sets `reasoning: true` on the model's `/v1/models` record so clients enable
reasoning-stream rendering. That is **all** it does. Single consumer:
`internal/server/api.go`.

It does **not** make the engine separate reasoning from content. If the model emits
`<think>…</think>` and the engine is not told to extract it, the tags arrive inside
`message.content` and every client renders them as prose. Setting `reasoning: true`
without the engine-side flag below produces the worst outcome: clients expect a
reasoning stream and get literal tags.

### Getting `reasoning_content` populated (llama.cpp)

```
--jinja --reasoning-format deepseek
```

`--reasoning-format` values: `none` (leave in `content`), `deepseek` (move to
`message.reasoning_content`), `deepseek-legacy` (keep the tags *and* populate the
field).

**This can silently do nothing.** llama.cpp decides whether a model produces
reasoning by inspecting the chat template. A template that never opens a think block
gives it no signal, so nothing is extracted even with the flag set — while the model
emits `<think>` anyway because it was trained to. That combination is real: AMD's
Instella-MoE ships DeepSeek-R1's template with the think-block priming removed.

Verify rather than assume — check whether `reasoning_content` is actually non-empty
in a response before setting `reasoning: true`.

Related: leave `sendLoadingState: false` on a reasoning model. llama-skein
synthesises its own `reasoning_content` for load-progress messages, which would
interleave with a real trace.

## Tools — the template must render them, or they are silently dropped

This is the trap that costs the most time.

llama.cpp passes an OpenAI `tools` array into the Jinja render context. **A template
that never reads `tools` produces a prompt without them, and nothing warns loudly.**
There is no schema-injection fallback on the prompt side. The model is then asked to
use tools it was never shown, and answers accordingly — typically "I don't have
access to that", which reads exactly like a model incapable of tool use.

Check it directly instead of inferring from behaviour:

```bash
curl -s localhost:$PORT/apply-template \
  -H 'Content-Type: application/json' \
  -d '{"messages":[{"role":"user","content":"list files"}],
       "tools":[{"type":"function","function":{"name":"run_shell",
                 "parameters":{"type":"object","properties":{"command":{"type":"string"}}}}}]}' \
  | grep -c run_shell
```

`0` means the tool catalogue never reached the model. A quick sanity signal: a
tools-less prompt for a one-line user turn is on the order of 60 characters.

llama.cpp does emit a warning for exactly this case —
`"Template supports tool calls but does not natively describe tools"` — but it is easy
to miss at default verbosity. Look for it.

### Parsing is usually already handled

For DeepSeek-R1-derived templates llama.cpp's autoparser recognises the marker set
(`<｜tool▁calls▁begin｜>`, `<｜tool▁call▁begin｜>`, `<｜tool▁sep｜>`,
`<｜tool▁call▁end｜>`, `<｜tool▁calls▁end｜>`) and builds both a parser and a lazy
grammar, mapping emitted markers back to OpenAI `tool_calls`. So the response side
generally works once the prompt side does.

**Detection is an exact substring match on the template source**
(`common/chat-diff-analyzer.cpp`). Reflowing or re-quoting the tool-call emit line —
even `{{ '` instead of `{{'` — disarms the parser, and the model's tool call then
arrives as unparsed text. Consequence for authoring:

> Build a tools-capable template by **inserting** into the model's own template.
> Never reformat it, and assert the detection substring survives.

### Managed templates

Templates live in `chat-templates/` in this repo and are deployed with the config,
not hand-copied onto hosts. A template `scp`'d to a host and referenced from `cmd` is
invisible, unreviewable, and drifts — the same failure mode
`docs-skein/config/README.md` documents for `config.yaml`.

Reference one with:

```yaml
    cmd: >
      /opt/engine/llama-server-x --port ${PORT} --model /models/x.gguf
      --jinja --chat-template-file /opt/engine/templates/x-tools.jinja
```

`chat-templates/instella-moe-tools.jinja` is a worked example: AMD's template
verbatim, plus a `tools` branch, plus a fix for an inherited bug where
`<｜tool▁calls▁end｜>` was emitted only for the second-and-later call so a single tool
call never closed its section.

**A template cannot make a model capable.** Instella-MoE, once it could actually see
a tool, produced correct arguments (`{"command": "ls"}`) — but AMD's published SFT
pipeline selects the `math_notool` split, skips every agentic subset, and strips
`system` messages, where tool definitions live. Fixing the plumbing buys a fair test,
not a capability. Measure before routing agent work to a model.

## `enginePath` — which binary the upgrade API manages

```yaml
enginePath: /opt/llamacpp-rocm-gfx110X/llama-server
```

`POST /api/system/upgrade` installs here. **Set it on any host running more than one
engine build.** Unset, the destination is discovered from running processes whose
basename is exactly `llama-server`, and conflicting candidates are an error.

Upgrade replaces the binary *and* copies the release's shared libraries into its
directory, so a wrong destination overwrites a self-contained engine's bundled
runtime — which the binary-only `.bak` cannot undo. Name a second, locally-built
engine something other than `llama-server` as defence in depth.

## `--ctx-size` drifts, in both directions

Several components write `--ctx-size` autonomously: opencode-skein's 413-overflow
adjuster and its TUI context dialog, skein's context sweep and its
`SetModelContext(Auto: true)` path.

`PATCH /api/config/models/{id}` now clamps to `max_fit_ctx` from `/api/fit` — the
smaller of VRAM-achievable and the model's **trained** context — and returns a
`warnings` entry when it does. Above the trained context the model extrapolates RoPE
and degrades in a way that looks like a bad model rather than a bad config.

The fit guard cannot catch that case: it clamps only when VRAM is exceeded, and an
over-trained context can still fit VRAM comfortably.

If a configured context looks wrong, compare `configured_ctx` against `max_fit_ctx`
in `/api/fit/{model}` before concluding anything about the model. And note the
distinction: `max_safe_ctx` is a *prompt budget* (output reserve and margin already
subtracted); `configured_ctx` is the hard `n_ctx`. Feeding one where the other is
expected produces systematically wrong numbers.
