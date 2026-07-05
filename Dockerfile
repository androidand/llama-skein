# Production image for llama-skein: builds the llama-skein binary and a
# Vulkan-enabled llama-server, and bundles them together so this image is
# runnable without any separate model-server setup.

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

# ---- llama.cpp build stage (ROCm/HIP backend) ------------------------------
# Builds llama-server from latest llama.cpp master with the ROCm (HIP)
# backend, targeting RDNA3 and RDNA4 (gfx1100, gfx1201) — extend
# AMDGPU_TARGETS if deploying on other AMD GPU generations.
FROM rocm/dev-ubuntu-24.04:7.2.4-complete AS llama-cpp-builder
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential cmake git ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
RUN git clone --depth=1 https://github.com/ggml-org/llama.cpp.git .
RUN cmake -B build \
      -DGGML_NATIVE=OFF -DGGML_HIP=ON -DGGML_VULKAN=OFF -DGGML_CUDA=OFF \
      -DAMDGPU_TARGETS="gfx1100;gfx1201" \
      -DGGML_HIP_NO_VMM=ON -DGGML_HIP_ROCWMMA_FATTN=ON \
      -DBUILD_SHARED_LIBS=OFF -DCMAKE_BUILD_TYPE=Release -DLLAMA_BUILD_TESTS=OFF \
    && cmake --build build --config Release -j"$(nproc)" --target llama-server

# ---- Runtime stage --------------------------------------------------------
# Same ROCm base as the builder: llama-server links against ROCm's runtime
# libraries (rocBLAS, hipBLAS, etc.), which aren't part of a plain Ubuntu
# image.
FROM rocm/dev-ubuntu-24.04:7.2.4-complete AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl \
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
