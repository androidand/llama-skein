# Production image for llama-skein: builds the llama-skein binary and a
# llama-server matching the chosen GPU backend, and bundles them together
# so this image is runnable without any separate model-server setup.
#
# BACKEND selects the llama-server build (default: vulkan, works on AMD/
# Intel/NVIDIA GPUs via Mesa with no vendor SDK required):
#   docker build --build-arg BACKEND=rocm   .   # AMD GPUs (ROCm/HIP)
#   docker build --build-arg BACKEND=cuda   .   # NVIDIA GPUs
#   docker build --build-arg BACKEND=vulkan .   # any Vulkan-capable GPU (default)
#   docker build --build-arg BACKEND=cpu    .   # no GPU

ARG BACKEND=vulkan

# ---- UI build stage ---------------------------------------------------
FROM node:20-slim AS ui-builder
WORKDIR /src/ui-svelte
COPY ui-svelte/package.json ui-svelte/package-lock.json* ./
RUN npm install
COPY ui-svelte/ ./
COPY internal/server/ui_dist /src/internal/server/ui_dist
RUN npm run build

# ---- Go build stage -----------------------------------------------------
FROM golang:1.26-bookworm AS go-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui-builder /src/internal/server/ui_dist ./internal/server/ui_dist

ARG COMMIT=docker
ARG BUILD_DATE
RUN test -n "$BUILD_DATE" || BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ); \
    UPSTREAM_VERSION=$(grep '^var UpstreamVersion' version.go | sed 's/.*= "\(.*\)"/\1/'); \
    SKEIN_VERSION=$(grep '^var SkeinVersion' version.go | sed 's/.*= "\(.*\)"/\1/'); \
    GOWORK=off CGO_ENABLED=0 go build \
      -ldflags="-X main.commit=${COMMIT} -X main.SkeinVersion=${SKEIN_VERSION} -X main.UpstreamVersion=${UPSTREAM_VERSION} -X main.date=${BUILD_DATE}" \
      -o /out/llama-skein .

# ---- llama.cpp builder bases, one per backend ------------------------------

FROM ubuntu:24.04 AS llama-cpp-builder-base-cpu
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential cmake git ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN git clone --depth=1 https://github.com/ggml-org/llama.cpp.git .

FROM ubuntu:24.04 AS llama-cpp-builder-base-vulkan
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential cmake git ca-certificates \
      libvulkan-dev glslang-tools spirv-tools vulkan-validationlayers glslc spirv-headers \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN git clone --depth=1 https://github.com/ggml-org/llama.cpp.git .

FROM docker.io/nvidia/cuda:12.9.1-devel-ubuntu24.04 AS llama-cpp-builder-base-cuda
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential cmake git ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN git clone --depth=1 https://github.com/ggml-org/llama.cpp.git .

# AMD's own ROCm image, not Ubuntu's natively-packaged rocm (lags several
# minor versions behind and has known issues on newer GPU generations).
FROM docker.io/rocm/dev-ubuntu-24.04:7.2.4-complete AS llama-cpp-builder-base-rocm
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential cmake git ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN git clone --depth=1 https://github.com/ggml-org/llama.cpp.git .

FROM llama-cpp-builder-base-${BACKEND} AS llama-cpp-builder
ARG BACKEND
# CUDA_ARCHITECTURES only matters for BACKEND=cuda; ignored otherwise.
ARG CUDA_ARCHITECTURES="60;61;75;86;89"
# AMDGPU_TARGETS only matters for BACKEND=rocm; ignored otherwise. Extend
# this list for other AMD GPU generations (gfx1100=RDNA3, gfx1201=RDNA4).
ARG AMDGPU_TARGETS="gfx1100;gfx1201"
RUN set -e; \
    case "$BACKEND" in \
      cpu) \
        CMAKE_FLAGS="-DGGML_VULKAN=OFF -DGGML_CUDA=OFF -DGGML_HIP=OFF" ;; \
      vulkan) \
        CMAKE_FLAGS="-DGGML_VULKAN=ON -DGGML_CUDA=OFF -DGGML_HIP=OFF" ;; \
      cuda) \
        CMAKE_FLAGS="-DGGML_VULKAN=OFF -DGGML_CUDA=ON -DGGML_HIP=OFF -DCMAKE_CUDA_ARCHITECTURES=${CUDA_ARCHITECTURES}" ;; \
      rocm) \
        CMAKE_FLAGS="-DGGML_VULKAN=OFF -DGGML_CUDA=OFF -DGGML_HIP=ON -DAMDGPU_TARGETS=${AMDGPU_TARGETS} -DGGML_HIP_NO_VMM=ON -DGGML_HIP_ROCWMMA_FATTN=ON -DCMAKE_POSITION_INDEPENDENT_CODE=ON" ;; \
      *) \
        echo "unknown BACKEND: $BACKEND" >&2; exit 1 ;; \
    esac; \
    cmake -B build -DGGML_NATIVE=OFF -DBUILD_SHARED_LIBS=OFF -DCMAKE_BUILD_TYPE=Release -DLLAMA_BUILD_TESTS=OFF $CMAKE_FLAGS && \
    cmake --build build --config Release -j"$(nproc)" --target llama-server

# ---- Runtime bases, one per backend -----------------------------------------

FROM ubuntu:24.04 AS runtime-base-cpu
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

FROM ubuntu:24.04 AS runtime-base-vulkan
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl libvulkan1 mesa-vulkan-drivers libgomp1 \
    && rm -rf /var/lib/apt/lists/*

FROM docker.io/nvidia/cuda:12.9.1-runtime-ubuntu24.04 AS runtime-base-cuda
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

# Same ROCm base as the builder: llama-server dynamically links ROCm's
# runtime libraries (rocBLAS, hipBLAS, etc.), not part of a plain Ubuntu
# image.
FROM docker.io/rocm/dev-ubuntu-24.04:7.2.4-complete AS runtime-base-rocm
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

FROM runtime-base-${BACKEND} AS runtime

WORKDIR /app
COPY --from=go-builder /out/llama-skein /usr/local/bin/llama-skein
COPY --from=llama-cpp-builder /src/build/bin/llama-server /usr/local/bin/llama-server
COPY config.example.yaml /etc/llama-skein/config.example.yaml
COPY config.docker-default.yaml /etc/llama-skein/config.yaml
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh
RUN mkdir -p /models

EXPOSE 8080
ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["-config", "/etc/llama-skein/config.yaml", "-listen", "0.0.0.0:8080"]
