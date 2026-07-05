# Production image for llama-skein, tailored for Tengil's git-source deploy
# flow (internal/gitbuilder in the tengil repo): a git URL/branch is cloned
# and built with `docker build .` at the repo root, and the resulting image
# becomes the LXC container's rootfs. See tengil's docs/ for the git
# install/upgrade flow this targets.
#
# One binary, one config file — matches llama-swap's own design goal, no
# GPU/CUDA toolchain needed to build or run llama-skein itself (it proxies
# to inference servers defined in config.yaml; those run as their own
# subprocesses/containers per that config, independent of this image).

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

# ---- llama.cpp build stage (Vulkan backend) --------------------------------
# Builds llama-server from latest llama.cpp master with the Vulkan backend
# (works on any Vulkan-capable GPU via Mesa RADV/ANV, no vendor SDK needed
# — chosen over CUDA/ROCm since it needs no proprietary driver stack inside
# the container, just the host's kernel driver + /dev/dri passthrough).
# This is what makes the image runnable out of the box: no separate
# "install llama.cpp" step for whoever deploys this.
FROM ubuntu:24.04 AS llama-cpp-builder
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential cmake git ca-certificates \
      libvulkan-dev glslang-tools spirv-tools vulkan-validationlayers glslc spirv-headers \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
RUN git clone --depth=1 https://github.com/ggml-org/llama.cpp.git .
RUN cmake -B build \
      -DGGML_NATIVE=OFF -DGGML_VULKAN=ON -DGGML_CUDA=OFF \
      -DBUILD_SHARED_LIBS=OFF -DCMAKE_BUILD_TYPE=Release -DLLAMA_BUILD_TESTS=OFF \
    && cmake --build build --config Release -j"$(nproc)" --target llama-server

# ---- Runtime stage --------------------------------------------------------
FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl libvulkan1 mesa-vulkan-drivers libgomp1 \
    && rm -rf /var/lib/apt/lists/*

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
