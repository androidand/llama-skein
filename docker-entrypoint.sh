#!/bin/sh
# Entrypoint for the Docker image. No model is bundled or assumed — set
# LLAMA_SKEIN_MODEL_URL (and optionally LLAMA_SKEIN_MODEL_PATH/_DIR) to
# fetch one on first start, or mount/manage a GGUF yourself.
set -e

# Some container runtimes don't populate /etc/hosts, breaking localhost
# resolution for the llama-server subprocess llama-skein manages.
if ! grep -q '^127\.0\.0\.1[[:space:]]' /etc/hosts 2>/dev/null; then
	{
		echo "127.0.0.1 localhost"
		echo "::1 localhost ip6-localhost ip6-loopback"
	} >> /etc/hosts
fi

MODEL_DIR="${LLAMA_SKEIN_MODEL_DIR:-/models}"
MODEL_PATH="${LLAMA_SKEIN_MODEL_PATH:-${MODEL_DIR}/default.gguf}"
MODEL_URL="${LLAMA_SKEIN_MODEL_URL:-}"

if [ -n "$MODEL_URL" ] && [ ! -f "$MODEL_PATH" ]; then
	echo "llama-skein: LLAMA_SKEIN_MODEL_URL set and ${MODEL_PATH} not found, downloading..."
	mkdir -p "$MODEL_DIR"
	curl -fL --retry 3 -o "${MODEL_PATH}.partial" "$MODEL_URL"
	mv "${MODEL_PATH}.partial" "$MODEL_PATH"
	echo "llama-skein: model downloaded to ${MODEL_PATH}"
elif [ -z "$MODEL_URL" ] && [ ! -f "$MODEL_PATH" ]; then
	echo "llama-skein: no model at ${MODEL_PATH} and LLAMA_SKEIN_MODEL_URL not set — starting anyway, but the bundled model entry in config.yaml will fail until you provide one."
fi

# The name/description clients see via /v1/models (e.g. opencode-skein's
# model picker) default to the GGUF filename so the served model is
# identifiable without guessing. LLAMA_SKEIN_MODEL_NAME overrides it.
if [ -n "$MODEL_URL" ]; then
	DERIVED_NAME=$(basename "$MODEL_URL" .gguf)
else
	DERIVED_NAME=$(basename "$MODEL_PATH" .gguf)
fi
MODEL_NAME="${LLAMA_SKEIN_MODEL_NAME:-$DERIVED_NAME}"

CONFIG_FILE="/etc/llama-skein/config.yaml"
if [ -f "$CONFIG_FILE" ]; then
	sed -i \
		-e "s|LLAMA_SKEIN_MODEL_NAME_PLACEHOLDER|${MODEL_NAME}|" \
		-e "s|LLAMA_SKEIN_MODEL_DESC_PLACEHOLDER|Serving ${MODEL_NAME} from ${MODEL_PATH}|" \
		"$CONFIG_FILE"
fi

exec llama-skein "$@"
