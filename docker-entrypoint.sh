#!/bin/sh
# Entrypoint for the bundled Docker image: downloads the default model on
# first start (skipped if it's already present, e.g. on a persistent
# rootfs/volume) so llama-skein is runnable with zero manual setup, then
# execs llama-skein itself.
set -e

# Some unmanaged/OCI-rootfs container setups don't populate /etc/hosts, so
# "localhost" (which llama-skein dials to reach the llama-server subprocess
# it spawns) falls through to a real DNS lookup and fails. Ensure the
# standard baseline entries are present before starting.
if ! grep -q '^127\.0\.0\.1[[:space:]]' /etc/hosts 2>/dev/null; then
	{
		echo "127.0.0.1 localhost"
		echo "::1 localhost ip6-localhost ip6-loopback"
	} >> /etc/hosts
fi

MODEL_DIR="${LLAMA_SKEIN_MODEL_DIR:-/models}"
MODEL_PATH="${LLAMA_SKEIN_MODEL_PATH:-${MODEL_DIR}/default.gguf}"
MODEL_URL="${LLAMA_SKEIN_MODEL_URL:-https://huggingface.co/unsloth/Qwen3.6-35B-A3B-GGUF/resolve/main/Qwen3.6-35B-A3B-UD-Q5_K_M.gguf}"

if [ ! -f "$MODEL_PATH" ]; then
	echo "llama-skein: default model not found at ${MODEL_PATH}, downloading..."
	mkdir -p "$MODEL_DIR"
	curl -fL --retry 3 -o "${MODEL_PATH}.partial" "$MODEL_URL"
	mv "${MODEL_PATH}.partial" "$MODEL_PATH"
	echo "llama-skein: default model downloaded to ${MODEL_PATH}"
fi

exec llama-skein "$@"
