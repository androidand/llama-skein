#!/usr/bin/env bash
# Install vLLM.
#
# NVIDIA CUDA: standard PyPI install works out of the box.
# AMD ROCm:    PyPI vLLM is CUDA-only. ROCm support requires building from
#              source (VLLM_TARGET_DEVICE=rocm pip install -e .) which takes
#              several hours and a full ROCm dev environment. On AMD hosts,
#              llama.cpp (already installed) is the practical default.
#              This script targets NVIDIA unless you pass --rocm-from-source.
#
# After running, add backend: vllm models to your llama-skein config using:
#   cmd: /opt/vllm-venv/bin/vllm serve <hf-model-id> --host 127.0.0.1 --port ${PORT}
set -euo pipefail

VENV="/opt/vllm-venv"
PYTHON=$(command -v python3 || true)

if [[ -z "$PYTHON" ]]; then
  echo "ERROR: python3 not found" >&2
  exit 1
fi

echo "Python: $PYTHON ($($PYTHON --version))"

# Ensure venv support is available (Ubuntu/Debian may need python3-venv)
if ! $PYTHON -m venv --help &>/dev/null; then
  echo "Installing python3-venv..."
  apt-get install -y python3-venv -q
fi

$PYTHON -m venv "$VENV"
"$VENV/bin/pip" install --upgrade pip -q

# Detect ROCm version to pick the right PyTorch wheel.
# PyTorch pre-built ROCm wheels currently top out at rocm6.3.
# For ROCm 7.x systems, rocm6.3 wheels work via HSA_OVERRIDE_GFX_VERSION.
ROCM_VERSION=$(cat /opt/rocm/.info/version 2>/dev/null | cut -d. -f1,2 || echo "")
if [[ -z "$ROCM_VERSION" ]]; then
  echo "WARNING: Could not detect ROCm version — defaulting to rocm6.3 PyTorch wheel"
  TORCH_ROCM="rocm6.3"
else
  MAJOR=$(echo "$ROCM_VERSION" | cut -d. -f1)
  if [[ "$MAJOR" -ge 7 ]]; then
    # ROCm 7.x: no native PyTorch wheel yet — use rocm6.3 with GFX override
    echo "ROCm $ROCM_VERSION detected (7.x) — using torch+rocm6.3 wheel (HSA_OVERRIDE_GFX_VERSION needed for new GPUs)"
    TORCH_ROCM="rocm6.3"
  else
    TORCH_ROCM="rocm${ROCM_VERSION}"
    echo "ROCm $ROCM_VERSION detected — using torch+${TORCH_ROCM}"
  fi
fi

echo "Installing torch (${TORCH_ROCM}) + vllm into $VENV ..."
"$VENV/bin/pip" install torch \
  --extra-index-url "https://download.pytorch.org/whl/${TORCH_ROCM}" -q

echo "Installing vllm ..."
"$VENV/bin/pip" install vllm -q

VERSION=$("$VENV/bin/python3" -c "import vllm; print(vllm.__version__)")
TORCH_VER=$("$VENV/bin/python3" -c "import torch; print(torch.__version__)")
echo ""
echo "OK: vllm $VERSION, torch $TORCH_VER installed at $VENV"
echo ""
echo "Example config entry (AMD ROCm / gfx1201):"
echo "  vllm-qwen3-8b:"
echo "    backend: vllm"
echo "    cmd: $VENV/bin/vllm serve Qwen/Qwen3-8B --host 127.0.0.1 --port \${PORT} --max-model-len 32768"
echo "    env:"
echo "      - HIP_VISIBLE_DEVICES=0"
echo "      - HSA_OVERRIDE_GFX_VERSION=11.0.0  # remove if gfx1201 works natively"
echo ""
echo "For NVIDIA CUDA, remove the HIP/HSA env vars and use CUDA_VISIBLE_DEVICES instead."
