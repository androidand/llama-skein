#!/usr/bin/env bash
# Tengil script-based installer for llama-skein.
#
# Replaces the OCI/git-build pipeline (Dockerfile -> podman build -> flatten
# to an LXC template tarball). That pipeline shipped the full ROCm SDK as the
# runtime base (a single image measured 21.9GB) and, combined with tengil
# never pruning old builds, one host accumulated 1.29TB of dangling image
# layers before anyone noticed. llama-skein is a single ~15MB Go binary plus,
# on AMD hosts, a handful of ROCm runtime libraries — it does not need to be
# containerized at all. This script creates a plain LXC directly and installs
# onto it, matching the manual cross-compile/scp/pct-push workflow this
# project has used by hand throughout its life, just automated.
#
# Live-validated 2026-07-19 on z4 (Proxmox host, Radeon PRO W7800/gfx1100):
# GPU passthrough, ROCm apt runtime install, GPU detection via
# /api/hardware, and a from-source build all confirmed working end-to-end in
# a fresh Ubuntu 24.04 LXC. Total container disk usage after install: ~2.4GB
# (vs 21.9GB for the OCI image this replaces).
#
# Tengil invokes this directly on the Proxmox HOST (not inside a container)
# via `bash -lc`, with CTID/HOSTNAME/TEMPLATE/CORE_COUNT/RAM_SIZE/DISK_SIZE
# etc. already exported as environment variables (see
# internal/api/install/script_host.go buildHostScriptEnv in the tengil repo).
# This script does not use the tteck/community-scripts build.func framework
# (no whiptail/pveversion dependency) — it drives `pct` directly, so none of
# tengil's interactive-tool mocking applies or is needed.
#
# Safe to run standalone too: every tengil-supplied variable has a sane
# default, so `CTID=199 ./tengil-install.sh` works from an interactive shell
# on the Proxmox host for manual testing.

set -euo pipefail

# ---- Configuration (tengil-supplied env vars, with standalone defaults) ----
CTID="${CTID:?CTID must be set (the target container ID)}"
HOSTNAME_="${HOSTNAME:-llama-skein-${CTID}}"
CORES="${CORE_COUNT:-${CORES:-4}}"
MEMORY_MB="${RAM_SIZE:-${MEMORY:-8192}}"
DISK_GB="${DISK_SIZE:-20}"
BRIDGE="${BRIDGE:-vmbr0}"
STORAGE="${STORAGE:-local-zfs}"
TEMPLATE_STORAGE="${TEMPLATE_STORAGE:-local}"
# AMD's ROCm apt repo only publishes Ubuntu (jammy/noble) packages, and the
# Dockerfile this replaces already targets ubuntu:24.04 for the same reason
# — keep both paths on the same base OS.
TEMPLATE="${TEMPLATE:-ubuntu-24.04-standard_24.04-2_amd64.tar.zst}"
REPO_URL="${LLAMA_SKEIN_REPO:-https://github.com/androidand/llama-skein.git}"
REPO_BRANCH="${LLAMA_SKEIN_BRANCH:-main}"
ROCM_APT_VERSION="${ROCM_APT_VERSION:-7.2.4}"
GO_VERSION="${GO_VERSION:-1.26.1}"

log() { echo "[tengil-install] $*" >&2; }

# ---- 1. Create the container -------------------------------------------
if pct status "$CTID" >/dev/null 2>&1; then
  log "CTID $CTID already exists — aborting rather than reusing/destroying it"
  exit 1
fi

if [ ! -f "/var/lib/vz/template/cache/${TEMPLATE}" ]; then
  log "downloading LXC template $TEMPLATE"
  pveam update >/dev/null 2>&1 || true
  pveam download "$TEMPLATE_STORAGE" "$TEMPLATE"
fi

log "creating CT $CTID ($HOSTNAME_): ${CORES} cores, ${MEMORY_MB}MB RAM, ${DISK_GB}GB disk"
pct create "$CTID" "${TEMPLATE_STORAGE}:vztmpl/${TEMPLATE}" \
  --hostname "$HOSTNAME_" \
  --cores "$CORES" \
  --memory "$MEMORY_MB" \
  --swap 512 \
  --rootfs "${STORAGE}:${DISK_GB}" \
  --net0 "name=eth0,bridge=${BRIDGE},firewall=0,ip=dhcp,type=veth" \
  --unprivileged 0 \
  --features nesting=1 \
  --onboot 1

# ---- 2. GPU passthrough (AMD/ROCm only for now) -------------------------
# Config lines confirmed working against z4's live LXC 102 (rocm-wedge
# investigation, 2026-07-16/17) and re-verified live during this script's
# own validation. Nvidia/Vulkan/CPU hosts need no device passthrough at all.
HAS_AMD_GPU=0
if [ -e /dev/kfd ]; then
  HAS_AMD_GPU=1
  log "AMD GPU detected on host (/dev/kfd present) — configuring passthrough"
  {
    # stat's %t/%T print major/minor in hex with no "0x" prefix (e.g. "e2"
    # for major 226) — bash's 16# base-conversion reads that directly.
    for dev in /dev/dri/card* /dev/dri/renderD*; do
      [ -e "$dev" ] || continue
      maj=$(( 16#$(stat -c '%t' "$dev") ))
      min=$(( 16#$(stat -c '%T' "$dev") ))
      echo "lxc.cgroup2.devices.allow: c ${maj}:${min} rwm"
    done
    echo "lxc.mount.entry: /dev/dri dev/dri none bind,optional,create=dir"
    kfd_maj=$(( 16#$(stat -c '%t' /dev/kfd) ))
    kfd_min=$(( 16#$(stat -c '%T' /dev/kfd) ))
    echo "lxc.cgroup2.devices.allow: c ${kfd_maj}:${kfd_min} rwm"
    echo "lxc.mount.entry: /dev/kfd dev/kfd none bind,optional,create=file"
  } >> "/etc/pve/lxc/${CTID}.conf"
fi

pct start "$CTID"
log "waiting for network"
for _ in $(seq 1 30); do
  pct exec "$CTID" -- getent hosts github.com >/dev/null 2>&1 && break
  sleep 2
done

# ---- 3. ROCm runtime (AMD only) — runtime libs, not the dev SDK ---------
# rocm-hip-runtime pulls in a build toolchain transitively (openmp-extras
# needs one) but is still ~1GB total vs. ~22GB for rocm/dev-ubuntu-*
# -complete, the base image the Dockerfile path used.
#
# Live validation hit a real conflict: Ubuntu's own repos ship a native
# rocminfo (5.7.1-3build1) that apt prefers over AMD's, so a plain install
# fails with "Depends: rocminfo (= X) but 5.7.1-3build1 is to be installed".
# Rather than hardcode version strings that will go stale as ROCm releases
# advance, parse apt's own error for the exact pins it says are needed and
# retry with those — self-correcting against whatever's current.
if [ "$HAS_AMD_GPU" -eq 1 ]; then
  log "installing ROCm ${ROCM_APT_VERSION} runtime (not the full SDK)"
  pct exec "$CTID" -- bash -c "
    set -euo pipefail
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -qq
    apt-get install -y -qq ca-certificates curl gnupg
    mkdir -p /etc/apt/keyrings
    curl -fsSL https://repo.radeon.com/rocm/rocm.gpg.key | gpg --dearmor -o /etc/apt/keyrings/rocm.gpg
    echo 'deb [arch=amd64 signed-by=/etc/apt/keyrings/rocm.gpg] https://repo.radeon.com/rocm/apt/${ROCM_APT_VERSION} noble main' \
      > /etc/apt/sources.list.d/rocm.list
    apt-get update -qq

    # -qq would suppress the unmet-dependency detail this fallback needs to
    # parse, so this first attempt must stay verbose.
    if ! apt-get install -y rocm-hip-runtime rocm-smi-lib > /tmp/rocm-apt.err 2>&1; then
      cat /tmp/rocm-apt.err >&2
      # Extract '<pkg> (= <version>) but ... is to be installed' lines from
      # apt's unmet-dependency report and pin exactly those packages.
      pins=\$(grep -oP 'Depends: \K\S+(?= \(= [^)]+\) but)' /tmp/rocm-apt.err)
      versions=\$(grep -oP 'Depends: \S+ \(= \K[^)]+(?=\) but)' /tmp/rocm-apt.err)
      if [ -z \"\$pins\" ]; then
        echo 'rocm-hip-runtime install failed with no parseable dependency pins' >&2
        exit 1
      fi
      pin_args=()
      while IFS= read -r pkg && IFS= read -r ver <&3; do
        pin_args+=(\"\${pkg}=\${ver}\")
      done < <(echo \"\$pins\") 3< <(echo \"\$versions\")
      apt-get install -y -qq \"\${pin_args[@]}\" rocm-hip-runtime rocm-smi-lib
    fi
    echo 'export PATH=\$PATH:/opt/rocm/bin' > /etc/profile.d/rocm.sh
  "
fi

# ---- 4. Build llama-skein from source -----------------------------------
log "installing Go ${GO_VERSION} and building llama-skein (${REPO_BRANCH})"
pct exec "$CTID" -- bash -c "
  set -euo pipefail
  export DEBIAN_FRONTEND=noninteractive
  apt-get install -y -qq git ca-certificates curl
  curl -fsSL https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz -o /tmp/go.tar.gz
  tar -C /usr/local -xzf /tmp/go.tar.gz
  rm -f /tmp/go.tar.gz
  export PATH=\$PATH:/usr/local/go/bin
  git clone --depth=1 --branch '${REPO_BRANCH}' '${REPO_URL}' /opt/llama-skein-src
  cd /opt/llama-skein-src
  go build -ldflags=\"-X main.commit=\$(git rev-parse --short HEAD)\" -o /usr/local/bin/llama-skein .
  # Go toolchain and module cache are build-time only; llama-skein itself is
  # a static Go binary with no runtime dependency on either.
  rm -rf /usr/local/go /root/go /opt/llama-skein-src/.git
"

mkdir_config_cmd='mkdir -p /etc/llama-skein /models && [ -f /etc/llama-skein/config.yaml ] || cat > /etc/llama-skein/config.yaml <<CFG
healthCheckTimeout: 120
logLevel: info
models: {}
CFG'
pct exec "$CTID" -- bash -c "$mkdir_config_cmd"

# ---- 5. systemd service ---------------------------------------------------
service_unit='[Unit]
Description=llama-skein inference proxy
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Environment=PATH=/opt/rocm/bin:/usr/bin:/bin
ExecStart=/usr/local/bin/llama-skein -config /etc/llama-skein/config.yaml -listen 0.0.0.0:8080
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target'
pct exec "$CTID" -- bash -c "cat > /etc/systemd/system/llama-skein.service <<'UNIT'
${service_unit}
UNIT
systemctl daemon-reload
systemctl enable --now llama-skein"

log "waiting for llama-skein to report healthy"
for _ in $(seq 1 15); do
  pct exec "$CTID" -- curl -sf -m 3 http://localhost:8080/v1/models >/dev/null 2>&1 && break
  sleep 2
done

if [ "$HAS_AMD_GPU" -eq 1 ]; then
  gfx=$(pct exec "$CTID" -- bash -c "export PATH=\$PATH:/opt/rocm/bin; rocminfo 2>/dev/null | awk '/^  Name:.*gfx/{print \$2; exit}'" || true)
  log "detected GPU target: ${gfx:-unknown} — fetch the matching llama-server engine via POST /api/system/upgrade once a model is configured (see contracts/llama-skein.openapi.json)"
fi

log "done. llama-skein is running in CT ${CTID} (${HOSTNAME_}) on port 8080."
