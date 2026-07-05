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

# ---- Runtime stage --------------------------------------------------------
FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=go-builder /out/llama-skein /usr/local/bin/llama-skein
COPY config.example.yaml /etc/llama-skein/config.yaml

EXPOSE 8080
ENTRYPOINT ["llama-skein"]
CMD ["-config", "/etc/llama-skein/config.yaml", "-listen", "0.0.0.0:8080"]
