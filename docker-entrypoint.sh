#!/bin/sh
# Entrypoint for the bundled Docker image. This image is generic and gets
# deployed on arbitrary hardware (from a Raspberry Pi to a workstation GPU),
# so it does NOT bundle or assume any particular model — there is no safe
# universal default size/quant. Set LLAMA_SKEIN_MODEL_URL (and optionally
# LLAMA_SKEIN_MODEL_PATH/_DIR) to have this entrypoint fetch a model on
# first start; leave it unset to manage models yourself (mount a GGUF,
# or edit config.yaml to point elsewhere).
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

exec llama-skein "$@"
