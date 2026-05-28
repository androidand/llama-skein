# Define variables for the application
APP_NAME = llama-skein
BUILD_DIR = build

# Get the current Git hash
GIT_HASH := $(shell git rev-parse --short HEAD)
ifneq ($(shell git status --porcelain),)
    # There are untracked changes
    GIT_HASH := $(GIT_HASH)+
endif

# Capture the current build date in RFC3339 format
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

# llama.cpp build metadata (injected from the target build context)
# llama_cpp_build: build number from llama.cpp (e.g. b5142)
# llama_cpp_git: short git hash of llama.cpp HEAD
# llama_cpp_date: date llama.cpp was last modified
# build_features: comma-separated list of enabled GPU features
LLAMA_CPP_BUILD ?= unknown
LLAMA_CPP_GIT   ?= unknown
LLAMA_CPP_DATE  ?= unknown
BUILD_FEATURES  ?=

# Default target: Builds binaries for both OSX and Linux
all: mac linux simple-responder

# Clean build directory
clean:
	rm -rf $(BUILD_DIR)

proxy/ui_dist/placeholder.txt:
	mkdir -p proxy/ui_dist
	touch $@

# use cached test results while developing
test-dev: proxy/ui_dist/placeholder.txt
	go test -short ./proxy/... ./internal/...
	staticcheck ./proxy/... ./internal/... || true

test: proxy/ui_dist/placeholder.txt
	go test -short -count=1 ./proxy/... ./internal/...

# for CI - full test (takes longer)
test-all: proxy/ui_dist/placeholder.txt
	go test -race -count=1 ./proxy/... ./internal/...

ui/node_modules:
	cd ui-svelte && npm install

# build react UI
ui: ui/node_modules
	cd ui-svelte && npm run build
	touch internal/server/ui_dist/placeholder.txt

# Build OSX binary
mac: ui
	@echo "Building Mac binary..."
	@LDFLAGS="-X main.commit=${GIT_HASH} -X main.version=local_${GIT_HASH} -X main.date=${BUILD_DATE} -X main.llamaCppBuild=${LLAMA_CPP_BUILD} -X main.llamaCppGit=${LLAMA_CPP_GIT} -X main.llamaCppDate=${LLAMA_CPP_DATE} -X main.buildFeatures=${BUILD_FEATURES}" \
		go build -ldflags="$${LDFLAGS}" -o $(BUILD_DIR)/$(APP_NAME)-darwin-arm64

# Build Linux binary
linux: linux-arm64 linux-amd64

linux-amd64: ui
	@echo "Building Linux AMD64 binary..."
	@LDFLAGS="-X main.commit=${GIT_HASH} -X main.version=local_${GIT_HASH} -X main.date=${BUILD_DATE} -X main.llamaCppBuild=${LLAMA_CPP_BUILD} -X main.llamaCppGit=${LLAMA_CPP_GIT} -X main.llamaCppDate=${LLAMA_CPP_DATE} -X main.buildFeatures=${BUILD_FEATURES}" \
		go build -ldflags="$${LDFLAGS}" -o $(BUILD_DIR)/$(APP_NAME)-linux-amd64

linux-arm64: ui
	@echo "Building Linux ARM64 binary..."
	@LDFLAGS="-X main.commit=${GIT_HASH} -X main.version=local_${GIT_HASH} -X main.date=${BUILD_DATE} -X main.llamaCppBuild=${LLAMA_CPP_BUILD} -X main.llamaCppGit=${LLAMA_CPP_GIT} -X main.llamaCppDate=${LLAMA_CPP_DATE} -X main.buildFeatures=${BUILD_FEATURES}" \
		go build -ldflags="$${LDFLAGS}" -o $(BUILD_DIR)/$(APP_NAME)-linux-arm64

# Build Windows binary
windows: ui
	@echo "Building Windows binary..."
	@LDFLAGS="-X main.commit=${GIT_HASH} -X main.version=local_${GIT_HASH} -X main.date=${BUILD_DATE} -X main.llamaCppBuild=${LLAMA_CPP_BUILD} -X main.llamaCppGit=${LLAMA_CPP_GIT} -X main.llamaCppDate=${LLAMA_CPP_DATE} -X main.buildFeatures=${BUILD_FEATURES}" \
		go build -ldflags="$${LDFLAGS}" -o $(BUILD_DIR)/$(APP_NAME)-windows-amd64.exe

# for testing proxy.Process
simple-responder:
	@echo "Building simple responder"
	GOOS=darwin GOARCH=arm64 go build -o $(BUILD_DIR)/simple-responder_darwin_arm64 cmd/simple-responder/simple-responder.go
	GOOS=linux GOARCH=amd64 go build -o $(BUILD_DIR)/simple-responder_linux_amd64 cmd/simple-responder/simple-responder.go

simple-responder-windows:
	@echo "Building simple responder for windows"
	GOOS=windows GOARCH=amd64 go build -o $(BUILD_DIR)/simple-responder.exe cmd/simple-responder/simple-responder.go

# Ensure build directory exists
$(BUILD_DIR):
	mkdir -p $(BUILD_DIR)

# Create a new release tag
release:
	@echo "Checking for unstaged changes..."
	@if [ -n "$(shell git status --porcelain)" ]; then \
		echo "Error: There are unstaged changes. Please commit or stash your changes before creating a release tag." >&2; \
		exit 1; \
	fi

# Get the highest tag in v{number} format, increment it, and create a new tag
	@highest_tag=$$(git tag --sort=-v:refname | grep -E '^v[0-9]+$$' | head -n 1 || echo "v0"); \
	new_tag="v$$(( $${highest_tag#v} + 1 ))"; \
	echo "tagging new version: $$new_tag"; \
	git tag "$$new_tag";

GOOS ?= $(shell go env GOOS 2>/dev/null || echo linux)
GOARCH ?= $(shell go env GOARCH 2>/dev/null || echo amd64)
wol-proxy: $(BUILD_DIR)
	@echo "Building wol-proxy"
	go build -o $(BUILD_DIR)/wol-proxy-$(GOOS)-$(GOARCH)-$(shell date +%Y-%m-%d) cmd/wol-proxy/wol-proxy.go

test-ui:
	cd ui-svelte && npm ci && npm run check && npm test

# llama.cpp ROCm build helpers
# These targets clone (if absent) and build llama.cpp with hardware-specific ROCm flags.
# They are meant to be run on the target Linux machines (proxmox, rocky), NOT cross-compiled.
#
# Usage:
#   make build-rocm-proxmox  # Build for RX 7800 XT (gfx1030, RDNA 3)
#   make build-rocm-rocky    # Build for RX 6700 XT (gfx1030, RDNA 1) confirm with `rocminfo`
#
# Prerequisites on target machine:
#   - ROCm 7.x SDK installed
#   - rocwmma-dev (for flash attention support on RDNA 3+)
#   - clang/hipcc from ROCm toolchain
#
# After build, replace the llama-server binary used by llama-swap:
#   cp build/llama-server-rocm-<target> /usr/local/bin/llama-server
# Then restart llama-swap.
#
# Note: This Makefile is part of the llama-swap fork for hardware optimization research.
# Build recipes validated in: docs/benchmarks/proxmox-rocm.md (+13.7% gain achieved)

LLAMA_CPP_DIR  ?= $(BUILD_DIR)/llama-cpp
LLAMA_CPP_URL  ?= https://github.com/ggerganov/llama.cpp.git

# Proxmox: RX 7800 XT (gfx1030, RDNA 3, Zen 4)
# Note: Research shows RX 7800 XT (gfx1030, RDNA 3) benefits most from HIP tuning.
# This recipe targets rdna3 (RX 7000 series) as the reference build; adjust for your GPU.
build-rocm-proxmox:
	@echo "Building llama.cpp for proxmox (RX 7800 XT, gfx1030, RDNA 3)..."
	@mkdir -p $(LLAMA_CPP_DIR)
	@if [ ! -d "$(LLAMA_CPP_DIR)/.git" ]; then \
		git clone --depth 1 $(LLAMA_CPP_URL) $(LLAMA_CPP_DIR); \
	fi
	@cd $(LLAMA_CPP_DIR) && cmake -S . -B build \
		-DGGML_HIP=ON \
		-DAMDGPU_TARGETS="amdgcn:rnacdna" \
		-DGGML_HIP_ROCWMMA_FATTN=ON \
		-DCMAKE_C_FLAGS="-march=znver4" \
		-DCMAKE_BUILD_TYPE=Release
	@cd $(LLAMA_CPP_DIR) && cmake --build build --config Release -- -j $$(nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 8)
	@echo "Output: $(LLAMA_CPP_DIR)/build/bin/llama-server"
	@echo "Verify: $(LLAMA_CPP_DIR)/build/bin/llama-server --version"
	@echo "Install: cp $(LLAMA_CPP_DIR)/build/bin/llama-server /usr/local/bin/llama-server"

# Rocky: RX 6700 XT (gfx1030, RDNA 1, Zen 2)
# Note: Research shows RX 6700 XT (gfx1030, RDNA 1) benefits less from HIP tuning.
# This recipe targets gfx803 (RDNA 1) as the reference build; adjust for your GPU.
build-rocm-rocky:
	@echo "Building llama.cpp for rocky (RX 6700 XT, gfx1030, RDNA 1)..."
	@mkdir -p $(LLAMA_CPP_DIR)
	@if [ ! -d "$(LLAMA_CPP_DIR)/.git" ]; then \
		git clone --depth 1 $(LLAMA_CPP_URL) $(LLAMA_CPP_DIR); \
	fi
	@cd $(LLAMA_CPP_DIR) && cmake -S . -B build \
		-DGGML_HIP=ON \
		-DAMDGPU_TARGETS="amdgcn:gfx803" \
		-DGGML_HIP_ROCWMMA_FATTN=ON \
		-DCMAKE_C_FLAGS="-march=znver2" \
		-DCMAKE_BUILD_TYPE=Release
	@cd $(LLAMA_CPP_DIR) && cmake --build build --config Release -- -j $$(nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 8)
	@echo "Output: $(LLAMA_CPP_DIR)/build/bin/llama-server"
	@echo "Verify: $(LLAMA_CPP_DIR)/build/bin/llama-server --version"
	@echo "Install: cp $(LLAMA_CPP_DIR)/build/bin/llama-server ~/.local/bin/llama-server"

# Phony targets
.PHONY: all clean ui mac windows simple-responder simple-responder-windows test test-all test-dev test-ui wol-proxy
.PHONE: linux linux-arm64 linux-amd64
.PHONY: build-rocm-proxmox build-rocm-rocky
