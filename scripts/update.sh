#!/usr/bin/env bash
# update.sh — Update svkexe-gateway to the latest version.
#
# Usage (on the target server):
#
#   sudo /opt/svkexe/scripts/update.sh
#
# Or from a remote machine:
#
#   curl -fsSL https://raw.githubusercontent.com/skrashevich/svkexe/main/scripts/update.sh | sudo bash
#
# Environment overrides:
#   SVKEXE_BRANCH       Branch/tag to update to (default: main)
#   SVKEXE_SRC_DIR      Checkout location (default: /opt/svkexe)
#   SKIP_RESTART=1       Build only, don't restart the service
#
# What it does:
#   1. Pulls the latest code from the remote.
#   2. Rebuilds the gateway binary.
#   3. Installs it to /usr/local/bin/svkexe-gateway.
#   4. Restarts the svkexe-gateway systemd service.
#
# Idempotent and safe to re-run.

set -euo pipefail

log()  { printf '\033[1;34m[update]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[update]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[update ERROR]\033[0m %s\n' "$*" >&2; exit 1; }

[[ "${EUID}" -eq 0 ]] || die "Must run as root. Try: sudo $0"

# ── Locate source ───────────────────────────────────────────────────────────

SVKEXE_BRANCH="${SVKEXE_BRANCH:-main}"
SVKEXE_SRC_DIR="${SVKEXE_SRC_DIR:-/opt/svkexe}"

# When piped via curl, there's no local checkout — use SVKEXE_SRC_DIR.
_raw_script="${BASH_SOURCE[0]:-}"
if [[ -n "${_raw_script}" && -f "${_raw_script}" ]]; then
    REPO_ROOT="$(cd "$(dirname "${_raw_script}")/.." && pwd)"
else
    REPO_ROOT="${SVKEXE_SRC_DIR}"
fi

[[ -f "${REPO_ROOT}/go.mod" ]] || die "Cannot find svkexe source at ${REPO_ROOT}"

# ── Constants ───────────────────────────────────────────────────────────────

BIN_NAME="svkexe-gateway"
INSTALL_PREFIX="/usr/local"
GO_INSTALL_DIR="/usr/local/go"
SERVICE_NAME="${BIN_NAME}.service"

export PATH="${GO_INSTALL_DIR}/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

# ── Step 1: Pull latest code ───────────────────────────────────────────────

OLD_COMMIT=""
if [[ -d "${REPO_ROOT}/.git" ]]; then
    OLD_COMMIT="$(git -C "${REPO_ROOT}" rev-parse HEAD 2>/dev/null || true)"
    log "Pulling latest changes (branch: ${SVKEXE_BRANCH})…"
    git -C "${REPO_ROOT}" fetch origin "${SVKEXE_BRANCH}"
    git -C "${REPO_ROOT}" checkout -q "${SVKEXE_BRANCH}"
    git -C "${REPO_ROOT}" reset --hard "origin/${SVKEXE_BRANCH}"
    COMMIT="$(git -C "${REPO_ROOT}" rev-parse --short HEAD)"
    log "Updated to commit ${COMMIT}."
else
    warn "${REPO_ROOT} is not a git repo — skipping pull, building from current state."
fi

# ── Step 1.5: Rebuild base image if build-image.sh changed ────────────────

if [[ -d "${REPO_ROOT}/.git" ]]; then
    if [[ -z "${OLD_COMMIT}" ]]; then
        log "No pre-pull commit recorded — skipping base image rebuild check."
    else
        NEW_COMMIT="$(git -C "${REPO_ROOT}" rev-parse HEAD)"
        if [[ "${OLD_COMMIT}" == "${NEW_COMMIT}" ]]; then
            log "No new commits — skipping base image rebuild check."
        elif git -C "${REPO_ROOT}" diff --name-only "${OLD_COMMIT}" "${NEW_COMMIT}" \
             | grep -q '^scripts/build-image\.sh$'; then
            log "scripts/build-image.sh changed — rebuilding svkexe-base image…"
            "${BASH}" "${REPO_ROOT}/scripts/build-image.sh"
        else
            log "scripts/build-image.sh unchanged — skipping base image rebuild."
        fi
    fi
else
    log "No git history available — skipping base image rebuild check."
fi

# ── Step 2: Build ───────────────────────────────────────────────────────────

command -v go &>/dev/null || die "Go not found. Install it or run the full installer first."

log "Building gateway binary…"
env HOME="/root" make -C "${REPO_ROOT}" build

# ── Step 3: Install binary ──────────────────────────────────────────────────

log "Installing binary to ${INSTALL_PREFIX}/bin/${BIN_NAME}…"
install -m 0755 "${REPO_ROOT}/bin/gateway" "${INSTALL_PREFIX}/bin/${BIN_NAME}"

# ── Step 4: Restart service ─────────────────────────────────────────────────

if [[ "${SKIP_RESTART:-0}" == "1" ]]; then
    log "SKIP_RESTART=1 — skipping service restart."
elif systemctl is-active --quiet "${SERVICE_NAME}" 2>/dev/null; then
    log "Restarting ${SERVICE_NAME}…"
    systemctl restart "${SERVICE_NAME}"
    sleep 1
    if systemctl is-active --quiet "${SERVICE_NAME}"; then
        log "Service restarted successfully."
    else
        warn "Service may have failed to start. Check: journalctl -u ${SERVICE_NAME} -n 30"
    fi
elif systemctl is-enabled --quiet "${SERVICE_NAME}" 2>/dev/null; then
    log "Service is not running. Starting ${SERVICE_NAME}…"
    systemctl start "${SERVICE_NAME}"
else
    warn "Systemd unit ${SERVICE_NAME} not found — binary installed but not started."
fi

# ── Summary ─────────────────────────────────────────────────────────────────

log "Update complete. Binary: ${INSTALL_PREFIX}/bin/${BIN_NAME}"
