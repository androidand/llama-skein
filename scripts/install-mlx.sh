#!/usr/bin/env bash
# Install mlx-lm for Apple Silicon Macs.
# After running, add backend: mlx models to your llama-skein config using:
#   cmd: /Users/andreas/.venv/mlx/bin/mlx_lm.server --host 127.0.0.1 --port ${PORT} --model <hf-repo>
set -euo pipefail

if [[ "$(uname -m)" != "arm64" ]]; then
  echo "ERROR: MLX only runs on Apple Silicon (arm64). Detected: $(uname -m)" >&2
  exit 1
fi

PYTHON=$(command -v python3.13 || command -v python3.12 || command -v python3.11 || command -v python3.10 || true)
if [[ -z "$PYTHON" ]]; then
  echo "ERROR: No Python 3.10+ found. Install with: brew install python@3.13" >&2
  exit 1
fi
echo "Using Python: $PYTHON ($($PYTHON --version))"

VENV="$HOME/.venv/mlx"
$PYTHON -m venv "$VENV" --system-site-packages
"$VENV/bin/pip" install --upgrade pip -q
"$VENV/bin/pip" install mlx-lm -q

VERSION=$("$VENV/bin/python3" -c "import mlx_lm; print(mlx_lm.__version__)")
echo "OK: mlx-lm $VERSION installed at $VENV"
echo "    Server binary: $VENV/bin/mlx_lm.server"
echo ""
echo "Example config entry:"
echo "  mlx-my-model:"
echo "    backend: mlx"
echo "    cmd: $VENV/bin/mlx_lm.server --host 127.0.0.1 --port \${PORT} --model mlx-community/Qwen3-8B-4bit"
