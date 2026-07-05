#!/bin/sh
# Entrypoint for the Docker image. No model is bundled or assumed — set
# LLAMA_SKEIN_MODEL_URL (and optionally LLAMA_SKEIN_MODEL_PATH/_DIR) to
# fetch one on first start, or mount/manage a GGUF yourself.
# LLAMA_SKEIN_CTX_SIZE overrides the default --ctx-size (32768).
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
CTX_SIZE="${LLAMA_SKEIN_CTX_SIZE:-32768}"

CONFIG_FILE="/etc/llama-skein/config.yaml"
if [ -f "$CONFIG_FILE" ] && grep -q 'LLAMA_SKEIN_CTX_SIZE_PLACEHOLDER' "$CONFIG_FILE"; then
	sed -i "s/LLAMA_SKEIN_CTX_SIZE_PLACEHOLDER/${CTX_SIZE}/" "$CONFIG_FILE"
fi

if [ -n "$MODEL_URL" ] && [ ! -f "$MODEL_PATH" ]; then
	echo "llama-skein: LLAMA_SKEIN_MODEL_URL set and ${MODEL_PATH} not found, downloading..."
	mkdir -p "$MODEL_DIR"
	curl -fL --retry 3 -o "${MODEL_PATH}.partial" "$MODEL_URL"
	mv "${MODEL_PATH}.partial" "$MODEL_PATH"
	echo "llama-skein: model downloaded to ${MODEL_PATH}"
elif [ -z "$MODEL_URL" ] && [ ! -f "$MODEL_PATH" ]; then
	echo "llama-skein: no model at ${MODEL_PATH} and LLAMA_SKEIN_MODEL_URL not set — starting anyway, but the bundled model entry in config.yaml will fail until you provide one."
fi

exec llama-skein "$@"
