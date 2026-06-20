#!/usr/bin/env bash
# llama-skein fleet auto-update — Mac-driven cross-deploy.
#
# The Mac (the only fleet host with Node + Go) is the single build host. It
# builds the FULL dashboard binary from a clean origin/main checkout — `make`
# runs the Svelte UI build + injects version ldflags — for each target arch,
# then pushes it to every reachable node with backup, health-check and rollback.
#
# Run on a schedule via launchd (scripts/launchd/com.skein.llama-fleet-update.plist)
# or by hand. Idempotent: if a node already runs the built rev it is left alone.
#
# Fleet (verified profiles — edit if topology changes):
#   m3       local Mac      /Users/andreas/bin/llama-swap-local   launchd com.llamaswap.m3      health :11435
#   proxmox  ssh + pct 1016 /usr/local/bin/llama-skein            systemd  llama-swap (system)  health :8080
#   rocky    ssh            ~/.local/bin/llama-skein              systemd  --user llama-skein    health :11435
#
# Why Mac-driven (not on-host self-build): proxmox/rocky have no Node, so they
# cannot run `make ui` to embed the dashboard. Building once on the Mac and
# shipping binaries keeps the dashboard everywhere with one toolchain.
set -euo pipefail

REPO="${LLAMA_SKEIN_REPO:-https://github.com/androidand/llama-skein.git}"
BRANCH="${LLAMA_SKEIN_BRANCH:-main}"
WORK="${LLAMA_SKEIN_WORK:-/tmp/llama-skein-fleet}"
STAMP_DIR="${LLAMA_SKEIN_STAMP_DIR:-$HOME/.cache/llama-skein-fleet}"
# Ensure Go + Node are reachable under launchd's minimal PATH.
export PATH="/opt/homebrew/bin:/usr/local/bin:$HOME/.nvm/versions/node/v24.4.1/bin:/usr/bin:/bin:$PATH"

log() { echo "[fleet-deploy $(date '+%Y-%m-%dT%H:%M:%S')] $*"; }
reachable() { ssh -o BatchMode=yes -o ConnectTimeout=5 "$1" true 2>/dev/null; }
mkdir -p "$STAMP_DIR"

# ---- build from a clean checkout (never the possibly-dirty working tree) ----
rm -rf "$WORK"
git clone --depth 1 --branch "$BRANCH" "$REPO" "$WORK" >/dev/null 2>&1
cd "$WORK"
REV="$(git rev-parse --short HEAD)"
log "built rev: $REV — running make (ui + ldflags) for linux-amd64 + darwin-arm64…"
make linux-amd64 >/dev/null   # build/llama-skein-linux-amd64 (proxmox/rocky)
make mac >/dev/null           # build/llama-skein-darwin-arm64 (m3/m5)
LINUX_BIN="$WORK/build/llama-skein-linux-amd64"
MAC_BIN="$WORK/build/llama-skein-darwin-arm64"
[ -s "$LINUX_BIN" ] && [ -s "$MAC_BIN" ] || { log "build did not produce both binaries; aborting."; exit 1; }

# already-deployed check per node via a local stamp (rev last pushed to that node)
needs_update() { [ "$(cat "$STAMP_DIR/$1.rev" 2>/dev/null || true)" != "$REV" ]; }
mark_done()    { echo "$REV" > "$STAMP_DIR/$1.rev"; }

# ---------------------------------- m3 (local) ----------------------------------
deploy_m3() {
  local bin="/Users/andreas/bin/llama-swap-local" job="com.llamaswap.m3" health="http://127.0.0.1:11435/v1/models"
  needs_update m3 || { log "m3 already at $REV"; return 0; }
  log "m3: installing $REV"
  cp -f "$bin" "$bin.bak" 2>/dev/null || true
  install -m 0755 "$MAC_BIN" "$bin"
  launchctl kickstart -k "gui/$(id -u)/$job"
  if health_local "$health"; then mark_done m3; log "m3 → $REV OK"; else
    log "m3 health FAILED — rolling back"; [ -f "$bin.bak" ] && { install -m 0755 "$bin.bak" "$bin"; launchctl kickstart -k "gui/$(id -u)/$job"; }; return 1; fi
}

# --------------------------------- proxmox (LXC 1016) ---------------------------
deploy_proxmox() {
  needs_update proxmox || { log "proxmox already at $REV"; return 0; }
  reachable proxmox || { log "proxmox unreachable — skip"; return 0; }
  log "proxmox: pushing $REV to LXC 1016"
  scp -q "$LINUX_BIN" proxmox:/tmp/llama-skein.new
  ssh proxmox "pct push 1016 /tmp/llama-skein.new /usr/local/bin/llama-skein.new && pct exec 1016 -- bash -c '
    cp -f /usr/local/bin/llama-skein /usr/local/bin/llama-skein.bak 2>/dev/null || true
    install -m 0755 /usr/local/bin/llama-skein.new /usr/local/bin/llama-skein
    systemctl restart llama-swap'"
  if ssh proxmox "pct exec 1016 -- bash -c 'for i in 1 2 3 4 5; do sleep 2; curl -fsS --max-time 4 http://127.0.0.1:8080/v1/models >/dev/null && exit 0; done; exit 1'"; then
    mark_done proxmox; log "proxmox → $REV OK"
  else
    log "proxmox health FAILED — rolling back"
    ssh proxmox "pct exec 1016 -- bash -c 'install -m 0755 /usr/local/bin/llama-skein.bak /usr/local/bin/llama-skein && systemctl restart llama-swap'"; return 1
  fi
}

# ------------------------------------- rocky ------------------------------------
deploy_rocky() {
  needs_update rocky || { log "rocky already at $REV"; return 0; }
  reachable rocky || { log "rocky unreachable — skip (cross-deploy when back online)"; return 0; }
  log "rocky: pushing $REV"
  scp -q "$LINUX_BIN" rocky:.local/bin/llama-skein.new
  ssh rocky 'cp -f ~/.local/bin/llama-skein ~/.local/bin/llama-skein.bak 2>/dev/null || true
    install -m 0755 ~/.local/bin/llama-skein.new ~/.local/bin/llama-skein
    systemctl --user restart llama-skein
    for i in 1 2 3 4 5; do sleep 2; curl -fsS --max-time 4 http://127.0.0.1:11435/v1/models >/dev/null && exit 0; done
    echo "rollback"; install -m 0755 ~/.local/bin/llama-skein.bak ~/.local/bin/llama-skein; systemctl --user restart llama-skein; exit 1' \
    && { mark_done rocky; log "rocky → $REV OK"; } || { log "rocky health FAILED — rolled back"; return 1; }
}

health_local() { for _ in 1 2 3 4 5; do sleep 2; curl -fsS --max-time 4 "$1" >/dev/null 2>&1 && return 0; done; return 1; }

rc=0
deploy_m3      || rc=1
deploy_proxmox || rc=1
deploy_rocky   || rc=1
log "done (rev $REV, exit $rc)"
exit $rc
