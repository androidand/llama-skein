> **llama-skein** — fork of [mostlygeek/llama-swap](https://github.com/mostlygeek/llama-swap) for the [Skein](https://github.com/androidand/local-ai) ecosystem.
> Adds: `/api/resources`, `/api/storage`, model lifecycle API, mDNS registration, ROCm targets, slot-cancel on disconnect, autoUnload.
> Ecosystem map: `ECOSYSTEM.md` (this repo) · `~/dev/skein/docs/ECOSYSTEM.md` (sibling checkout, if present)

---

![llama-swap header image](docs/assets/hero3.webp)
![GitHub Actions Workflow Status](https://img.shields.io/github/actions/workflow/status/androidand/llama-skein/go-ci.yml)
![GitHub Repo stars](https://img.shields.io/github/stars/androidand/llama-skein)

# llama-skein

llama-skein is the LLM inference proxy for the [Skein](https://github.com/androidand/local-ai) ecosystem: a single static Go binary that fronts llama.cpp (and other OpenAI-compatible engines), hot-swaps models on demand, and exposes hardware-aware resource/storage/lifecycle APIs plus mDNS discovery so the rest of the ecosystem can find it on the LAN without hardcoded addresses. It is a fork of [mostlygeek/llama-swap](https://github.com/mostlygeek/llama-swap) — everything llama-swap does, this repo does too, plus the extensions below.

## Divergence from upstream

llama-skein tracks `mostlygeek/llama-swap` via `make upstream-check` / rebase (see `ECOSYSTEM.md`) and adds:

- `GET /api/resources` — unified GPU/CPU/RAM snapshot
- `GET /api/storage` — model dir disk usage
- `POST /api/models/pull` — HuggingFace model download with streaming progress
- `DELETE /api/models/:id` — unload + delete weight file
- `PATCH /api/config/models/:id` — live model config patch (ctx-size, n_gpu_layers)
- `POST`/`DELETE /api/config/models` — add/remove models at runtime
- `GET /api/config/info` — config path + file existence
- mDNS registration (`_llamaswap._tcp.local.`) on startup
- Ollama-compat endpoints
- HTTP 413 on context size exceeded
- `context_length` / `max_output_tokens` in `/v1/models`
- `autoUnload` config for model groups
- Slot-cancel on client disconnect (prevents zombie GPU allocations)
- ROCm build targets for AMD GPUs on inference hosts

See `ECOSYSTEM.md` for the full ecosystem map and upstream-sync workflow, and `contracts/llama-skein.openapi.json` for the control-plane API contract (design-first source of truth; not duplicated here).

Everything below this point is inherited from upstream llama-swap and describes functionality both projects share.

## Features (inherited from llama-swap):

- ✅ Easy to deploy and configure: one binary, one configuration file. no external dependencies
- ✅ On-demand model switching
- ✅ Use any local OpenAI compatible server (llama.cpp, vllm, tabbyAPI, stable-diffusion.cpp, etc.)
  - future proof, upgrade your inference servers at any time.
- ✅ OpenAI API supported endpoints:
  - `v1/completions`
  - `v1/chat/completions`
  - `v1/responses`
  - `v1/embeddings`
  - `v1/models` - list available models
  - `v1/audio/speech` ([#36](https://github.com/mostlygeek/llama-swap/issues/36))
  - `v1/audio/transcriptions` ([docs](https://github.com/mostlygeek/llama-swap/issues/41#issuecomment-2722637867))
  - `v1/audio/voices`
  - `v1/images/generations`
  - `v1/images/edits`
- ✅ Anthropic API supported endpoints:
  - `v1/messages`
  - `v1/messages/count_tokens`
- ✅ llama-server (llama.cpp) supported endpoints
  - `v1/rerank`, `v1/reranking`, `/rerank`
  - `/infill` - for code infilling
  - `/completion` - for completion endpoint
- ✅ SDAPI via [stable-diffusion.cpp's server](https://github.com/leejet/stable-diffusion.cpp/tree/master/examples/server)
  - `/sdapi/v1/txt2img`
  - `/sdapi/v1/img2img`
  - `/sdapi/v1/loras` - requires `model` in request body to fetch the correct loras
- ✅ llama-swap API (inherited; llama-skein adds its own control-plane API on top, see Divergence above)
  - `/ui` - web UI
  - `/upstream/:model_id` - direct access to upstream server ([demo](https://github.com/mostlygeek/llama-swap/pull/31))
  - `/running` - list currently running models ([#61](https://github.com/mostlygeek/llama-swap/issues/61))
  - `POST /api/models/unload` - manually unload all running models ([#58](https://github.com/mostlygeek/llama-swap/issues/58))
  - `POST /api/models/unload/:model_id` - unload a specific model
  - `/logs` - remote log monitoring
    - `GET /logs` returns buffered plain text logs.
      - If `Accept: text/html` is sent, `/logs` redirects to `/ui/`.
    - `GET /logs/stream` keeps the connection open for live log streaming.
      - Stream endpoints send buffered history first by default; add `?no-history` to stream only new lines.
    - `GET /logs/stream/proxy` streams proxy logs only.
    - `GET /logs/stream/upstream` streams upstream process logs only.
    - `GET /logs/stream/{model_id}` streams logs for one model (including IDs with slashes, like `author/model`).
  - `/health` - just returns "OK"
  - `/metrics` - system and GPU metrics for prometheus
- ✅ API Key support - define keys to restrict access to API endpoints
- ✅ Customizable
  - Run concurrent models with a custom DSL swap matrix ([#643](https://github.com/mostlygeek/llama-swap/issues/643))
  - Automatic unloading of models after timeout by setting a `ttl`
  - Docker and Podman support using `cmd` and `cmdStop` together
  - Preload models on startup with `hooks` ([#235](https://github.com/mostlygeek/llama-swap/pull/235))
  - Apply filters to requests to control inference with `stripParams`, `setParams` and `setParamsByID`

### Web UI

llama-swap includes a real time web interface with a playground for testing out all sorts of local models:

<img width="1125" height="876" alt="image" src="https://github.com/user-attachments/assets/8ee41947-97af-463d-b0f0-8e9c478fac07" />

View detailed token metrics:

<img width="1111" height="515" alt="image" src="https://github.com/user-attachments/assets/64bfb280-d7a3-4126-971a-a128fd40410c" />

Inspect request and responses:

<img width="1111" height="720" alt="image" src="https://github.com/user-attachments/assets/24fe4aca-1448-4d7c-b9e8-a967589bda6c" />

Manually load and unload models:

<img width="1109" height="719" alt="image" src="https://github.com/user-attachments/assets/02b1e1f2-abd0-4050-84ae-facd66ff01c4" />

Real time log streaming:

<img width="1107" height="559" alt="image" src="https://github.com/user-attachments/assets/39669a10-cff2-409e-836a-5bad8bd0140c" />

## Installation

llama-skein is a fork and does not publish to upstream's Docker/Homebrew/WinGet channels — those install **upstream llama-swap**, which does not have the fork extensions listed above. The supported way to run llama-skein is building from source.

### Building from source (primary install path)

1. Building requires Go and Node.js (for the UI, in `ui-svelte/`).
1. `git clone https://github.com/androidand/llama-skein.git`
1. `cd llama-skein && make clean all`
1. look in the `build/` subdirectory for the `llama-skein` binary

### Upstream channels (llama-swap, not this fork)

The channels below install **mostlygeek/llama-swap**, not llama-skein. They do not contain `/api/resources`, `/api/storage`, the model lifecycle API, mDNS registration, ROCm targets, or any other fork extension. Listed here only so it's clear what they are, in case you land on this README while looking for upstream:

- Docker: `ghcr.io/mostlygeek/llama-swap` ([images](https://github.com/mostlygeek/llama-swap/pkgs/container/llama-swap))
- Homebrew: `brew tap mostlygeek/llama-swap && brew install llama-swap`
- WinGet: `winget install llama-swap` (community-maintained, not official)
- Pre-built binaries: [mostlygeek/llama-swap releases](https://github.com/mostlygeek/llama-swap/releases)

## Configuration

```yaml
# minimum viable config.yaml

models:
  model1:
    cmd: llama-server --port ${PORT} --model /path/to/model.gguf
```

That's all you need to get started:

1. `models` - holds all model configurations
2. `model1` - the ID used in API calls
3. `cmd` - the command to run to start the server.
4. `${PORT}` - an automatically assigned port number

Almost all configuration settings are optional and can be added one step at a time:

- Advanced features
  - `matrix` to run concurrent models with a custom swap logic DSL
  - `hooks` to run things on startup
  - `macros` reusable snippets
- Model customization
  - `ttl` to automatically unload models
  - `aliases` to use familiar model names (e.g., "gpt-4o-mini")
  - `env` to pass custom environment variables to inference servers
  - `cmdStop` gracefully stop Docker/Podman containers
  - `useModelName` to override model names sent to upstream servers
  - `${PORT}` automatic port variables for dynamic port assignment
  - `filters` rewrite parts of requests before sending to the upstream server

See the [configuration documentation](docs/configuration.md) for all options.

Running MLX models on Apple Silicon? See
[Apple Silicon / MLX](docs/macos-mlx.md) for `backend: mlx` setup, the Metal
memory ceiling (`iogpu.wired_limit_mb`) that governs what fits, and how to
recover a host that's run low on memory.

## How does llama-skein work?

When a request is made to an OpenAI compatible endpoint, llama-skein will extract the `model` value and load the appropriate server configuration to serve it. If the wrong upstream server is running, it will be replaced with the correct one. This is where the "swap" part comes in (inherited from llama-swap). The upstream server is automatically swapped to handle the request correctly.

In the most basic configuration llama-skein handles one model at a time. For more advanced use cases, using a `matrix` allows multiple models to be loaded at the same time. You have complete control over how your system resources are used.

## Reverse Proxy Configuration (nginx)

If you deploy llama-skein behind nginx, disable response buffering for streaming endpoints. By default, nginx buffers responses which breaks Server‑Sent Events (SSE) and streaming chat completion. ([mostlygeek/llama-swap#236](https://github.com/mostlygeek/llama-swap/issues/236))

Recommended nginx configuration snippets:

```nginx
# SSE for UI events/logs
location /api/events {
    proxy_pass http://your-llama-skein-backend;
    proxy_buffering off;
    proxy_cache off;
}

# Streaming chat completions (stream=true)
location /v1/chat/completions {
    proxy_pass http://your-llama-skein-backend;
    proxy_buffering off;
    proxy_cache off;
}
```

As a safeguard, llama-skein also sets `X-Accel-Buffering: no` on SSE responses. However, explicitly disabling `proxy_buffering` at your reverse proxy is still recommended for reliable streaming behavior.

## Monitoring Logs on the CLI

```sh
# sends up to the last 10KB of logs
$ curl http://host/logs

# streams combined logs
curl -Ns http://host/logs/stream

# stream llama-skein's proxy status logs
curl -Ns http://host/logs/stream/proxy

# stream logs from upstream processes that llama-skein loads
curl -Ns http://host/logs/stream/upstream

# stream logs only from a specific model
curl -Ns http://host/logs/stream/{model_id}

# stream and filter logs with linux pipes
curl -Ns http://host/logs/stream | grep 'eval time'

# appending ?no-history will disable sending buffered history first
curl -Ns 'http://host/logs/stream?no-history'
```

## Do I need to use llama.cpp's server (llama-server)?

Any OpenAI compatible server would work. llama-swap (and this fork) was originally designed for llama-server and it is the best supported.

For Python based inference servers like vllm or tabbyAPI it is recommended to run them via podman or docker. This provides clean environment isolation as well as responding correctly to `SIGTERM` signals for proper shutdown.

## Credit

llama-skein is a fork of [mostlygeek/llama-swap](https://github.com/mostlygeek/llama-swap) by Benson Wong. All credit for the original design and the majority of the codebase goes to the upstream project and its contributors. If you don't need the Skein-specific extensions, use upstream llama-swap directly — it's the more widely used, more broadly supported project.
