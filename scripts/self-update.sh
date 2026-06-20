#!/usr/bin/env bash
# llama-skein fleet self-update (source-build model).
#
# Pulls the latest source, rebuilds the llama-skein proxy binary, swaps it in
# with a health-checked, rollback-on-failure restart. Designed to run unattended
# from a systemd timer (Linux) — see scripts/systemd/llama-skein-update.{service,timer}.
#
# This matches the fleet's actual deployment (docs-skein/deploy/llama-skein.md):
# nodes build from source, they do NOT consume goreleaser release binaries.
# The llama-skein proxy is plain Go (no CGO/ROCm) so it builds anywhere with a
# Go toolchain — ROCm is a llama.cpp concern, not this binary's.
#
# Hosts WITHOUT a Go toolchain (e.g. rocky) cannot self-build: their binary is
# cross-compiled on a Mac and scp'd. Drive those from the build host instead
# (see scripts/cross-deploy-rocky.sh — TODO), not from this on-host updater.
#
# Config via env (all optional; defaults suit a system-scope install):
#   LLAMA_SKEIN_REPO           git URL              (default: GitHub fork)
#   LLAMA_SKEIN_BRANCH         branch               (default: main)
#   LLAMA_SKEIN_BIN            installed binary     (default: /usr/local/bin/llama-skein)
#   LLAMA_SKEIN_SERVICE        systemd unit name    (default: llama-skein)
#   LLAMA_SKEIN_SERVICE_SCOPE  system | user        (default: system)
#   LLAMA_SKEIN_HEALTH_URL     post-restart probe   (default: http://127.0.0.1:11435/v1/models)
#   LLAMA_SKEIN_WORKDIR        build checkout dir   (default: /var/tmp/llama-skein-selfupdate)
set -euo pipefail

REPO_URL="${LLAMA_SKEIN_REPO:-https://github.com/androidand/llama-skein.git}"
BRANCH="${LLAMA_SKEIN_BRANCH:-main}"
BIN="${LLAMA_SKEIN_BIN:-/usr/local/bin/llama-skein}"
SERVICE="${LLAMA_SKEIN_SERVICE:-llama-skein}"
SERVICE_SCOPE="${LLAMA_SKEIN_SERVICE_SCOPE:-system}"
HEALTH_URL="${LLAMA_SKEIN_HEALTH_URL:-http://127.0.0.1:11435/v1/models}"
WORKDIR="${LLAMA_SKEIN_WORKDIR:-/var/tmp/llama-skein-selfupdate}"

log() { echo "[llama-skein-update] $*"; }

SUDO=""
if [ "$SERVICE_SCOPE" != "user" ] && [ "$(id -u)" -ne 0 ]; then SUDO="sudo"; fi
sc() { if [ "$SERVICE_SCOPE" = "user" ]; then systemctl --user "$@"; else $SUDO systemctl "$@"; fi; }
as_root() { if [ "$SERVICE_SCOPE" = "user" ]; then "$@"; else $SUDO "$@"; fi; }

# Make a standard Go install discoverable even from a minimal systemd PATH
# (proxmox keeps go at /usr/local/go/bin, which isn't on the unit's default PATH).
for godir in /usr/local/go/bin "$HOME/go/bin" /opt/go/bin; do
  case ":$PATH:" in *":$godir:"*) ;; *) [ -x "$godir/go" ] && PATH="$godir:$PATH" ;; esac
done
export PATH

command -v go >/dev/null 2>&1 || { log "no Go toolchain on this host — cannot self-build (cross-deploy from a Mac instead). Aborting."; exit 0; }
command -v git >/dev/null 2>&1 || { log "git required. Aborting."; exit 1; }

mkdir -p "$WORKDIR"
if [ -d "$WORKDIR/repo/.git" ]; then
  git -C "$WORKDIR/repo" fetch --depth 1 origin "$BRANCH"
  git -C "$WORKDIR/repo" reset --hard "origin/$BRANCH" >/dev/null
else
  rm -rf "$WORKDIR/repo"
  git clone --depth 1 --branch "$BRANCH" "$REPO_URL" "$WORKDIR/repo"
fi
cd "$WORKDIR/repo"

NEW_REV="$(git rev-parse --short HEAD)"
STAMP="${WORKDIR}/installed.rev"
if [ -f "$STAMP" ] && [ "$(cat "$STAMP")" = "$NEW_REV" ]; then
  log "already at $NEW_REV — nothing to do."
  exit 0
fi

log "building $NEW_REV …"
TMPBIN="$WORKDIR/llama-skein.new"
# go:embed ui_dist needs the dir present; a fresh clone ships only a tracked
# placeholder (the built UI is gitignored). Stub it like the canonical deploy so
# the build never fails on a clean checkout.
#
# NOTE: this is a PLAIN build (no `make ui`, no ldflags) — it matches the fleet's
# quick-deploy and therefore produces a binary WITHOUT the embedded web dashboard
# and with placeholder version metadata (commit=abcd1234). If the dashboard is
# wanted, switch this to `make` (requires Node on the host for `make ui`).
mkdir -p proxy/ui_dist && [ -e proxy/ui_dist/index.html ] || : > proxy/ui_dist/index.html
CGO_ENABLED=0 go build -o "$TMPBIN" .

log "swapping binary $BIN (backup -> ${BIN}.bak) and restarting $SERVICE ($SERVICE_SCOPE)…"
as_root cp -f "$BIN" "${BIN}.bak" 2>/dev/null || true
as_root install -m 0755 "$TMPBIN" "$BIN"
sc restart "$SERVICE"

# Health check with a few retries; roll back the binary on failure.
ok=0
for _ in 1 2 3 4 5; do
  sleep 2
  if curl -fsS --max-time 4 "$HEALTH_URL" >/dev/null 2>&1; then ok=1; break; fi
done

if [ "$ok" -eq 1 ]; then
  echo "$NEW_REV" > "$STAMP"
  log "updated to $NEW_REV and healthy."
else
  log "health check failed after restart — rolling back."
  if [ -f "${BIN}.bak" ]; then as_root install -m 0755 "${BIN}.bak" "$BIN"; sc restart "$SERVICE"; fi
  exit 1
fi
