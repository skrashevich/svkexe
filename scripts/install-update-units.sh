#!/usr/bin/env bash
# install-update-units.sh — Install the systemd plumbing behind the web UI's
# "update" button.
#
# Usage (on the target server):
#
#   sudo /opt/svkexe/scripts/install-update-units.sh
#
# Environment overrides:
#   SVKEXE_SRC_DIR         Checkout the update runs from
#                          (default: the repo this script lives in, else /opt/svkexe)
#   SVKEXE_BRANCH          Branch/tag the update tracks (default: main)
#   DATA_DIR               Gateway state directory (default: /var/lib/svkexe)
#   SVKEXE_UPDATE_TRIGGER  Request file (default: ${DATA_DIR}/update.trigger)
#   SVKEXE_UPDATE_STATUS   Machine-readable status file
#                          (default: ${DATA_DIR}/update-status.json)
#   SVKEXE_UPDATE_LOG      Full run log (default: ${DATA_DIR}/update.log)
#   SVKEXE_UPDATE_WATCHER  Marker proving a watcher is installed
#                          (default: ${DATA_DIR}/update-watcher)
#   SVKEXE_SERVICE_USER    User that must be able to read the marker
#                          (default: svkexe)
#   SVKEXE_SYSTEMD_DIR     Unit directory (default: /etc/systemd/system)
#
# This is the single source of truth for the self-update units. Both
# scripts/install.sh and scripts/update.sh call it, so an operator who upgrades
# the documented way picks up fixes to the update plumbing itself instead of
# only on a fresh install.
#
# Idempotent and safe to re-run.

set -euo pipefail

log()  { printf '\033[1;34m[update-units]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[update-units]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[update-units ERROR]\033[0m %s\n' "$*" >&2; exit 1; }

[[ "${EUID}" -eq 0 ]] || die "Must run as root. Try: sudo $0"

# ── Paths ────────────────────────────────────────────────────────────────────

# When piped via curl there is no local checkout to derive the source tree
# from; fall back to the documented install location.
_raw_script="${BASH_SOURCE[0]:-}"
if [[ -z "${SVKEXE_SRC_DIR:-}" && -n "${_raw_script}" && -f "${_raw_script}" ]]; then
    SVKEXE_SRC_DIR="$(cd "$(dirname "${_raw_script}")/.." && pwd)"
fi
SVKEXE_SRC_DIR="${SVKEXE_SRC_DIR:-/opt/svkexe}"
SVKEXE_BRANCH="${SVKEXE_BRANCH:-main}"

DATA_DIR="${DATA_DIR:-/var/lib/svkexe}"
SVKEXE_UPDATE_TRIGGER="${SVKEXE_UPDATE_TRIGGER:-${DATA_DIR}/update.trigger}"
SVKEXE_UPDATE_STATUS="${SVKEXE_UPDATE_STATUS:-${DATA_DIR}/update-status.json}"
SVKEXE_UPDATE_LOG="${SVKEXE_UPDATE_LOG:-${DATA_DIR}/update.log}"
SVKEXE_UPDATE_WATCHER="${SVKEXE_UPDATE_WATCHER:-${DATA_DIR}/update-watcher}"
SVKEXE_SERVICE_USER="${SVKEXE_SERVICE_USER:-svkexe}"
SVKEXE_SYSTEMD_DIR="${SVKEXE_SYSTEMD_DIR:-/etc/systemd/system}"

UPDATE_PATH_UNIT="svkexe-update.path"
UPDATE_SERVICE_UNIT="svkexe-update.service"
UPDATE_PATH_FILE="${SVKEXE_SYSTEMD_DIR}/${UPDATE_PATH_UNIT}"
UPDATE_SERVICE_FILE="${SVKEXE_SYSTEMD_DIR}/${UPDATE_SERVICE_UNIT}"

# The gateway runs as an unprivileged user and only reads the marker; make it
# group-readable by that user when it exists (it does not on dev boxes).
_share_with_service_user() {
    local path="$1"
    [[ -e "${path}" ]] || return 0
    chmod 0644 "${path}" 2>/dev/null || true
    if getent group "${SVKEXE_SERVICE_USER}" >/dev/null 2>&1; then
        chown "root:${SVKEXE_SERVICE_USER}" "${path}" 2>/dev/null || true
    elif getent passwd "${SVKEXE_SERVICE_USER}" >/dev/null 2>&1; then
        chown "${SVKEXE_SERVICE_USER}" "${path}" 2>/dev/null || true
    fi
}

# ── Unit files ───────────────────────────────────────────────────────────────

install -d -m 0755 "${SVKEXE_SYSTEMD_DIR}"

# PathExists (not PathChanged): the trigger is created by the gateway, which
# may write it while the .path unit is stopped or mid-daemon-reload. PathExists
# fires on activation too if the file is already there, so no click is ever
# lost. The unit re-arms as soon as the file disappears — update.sh consumes
# the trigger itself before doing any work, so a single click can't loop, and
# update.sh can tell a fresh request apart from one left over from a reboot.
log "Installing systemd unit ${UPDATE_PATH_FILE}…"
cat >"${UPDATE_PATH_FILE}" <<EOF
[Unit]
Description=Watch for svkexe self-update requests
Documentation=https://github.com/skrashevich/svkexe

[Path]
PathExists=${SVKEXE_UPDATE_TRIGGER}
Unit=${UPDATE_SERVICE_UNIT}

[Install]
WantedBy=multi-user.target
EOF

# This unit deliberately carries none of the gateway's hardening: it runs
# apt-get, rebuilds Incus images and calls systemctl, so it needs real root
# with an unrestricted filesystem view. It is started by the .path unit only,
# hence no [Install] section.
log "Installing systemd unit ${UPDATE_SERVICE_FILE}…"
cat >"${UPDATE_SERVICE_FILE}" <<EOF
[Unit]
Description=svkexe self-update
Documentation=https://github.com/skrashevich/svkexe
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
User=root
Group=root
WorkingDirectory=${SVKEXE_SRC_DIR}
Environment=HOME=/root
Environment=SVKEXE_SRC_DIR=${SVKEXE_SRC_DIR}
Environment=SVKEXE_BRANCH=${SVKEXE_BRANCH}
Environment=SVKEXE_UPDATE_TRIGGER=${SVKEXE_UPDATE_TRIGGER}
Environment=SVKEXE_UPDATE_STATUS=${SVKEXE_UPDATE_STATUS}
Environment=SVKEXE_UPDATE_LOG=${SVKEXE_UPDATE_LOG}
Environment=SVKEXE_UPDATE_WATCHER=${SVKEXE_UPDATE_WATCHER}
Environment=SVKEXE_SERVICE_USER=${SVKEXE_SERVICE_USER}
ExecStart=/bin/bash ${SVKEXE_SRC_DIR}/scripts/update.sh
# A cold run rebuilds the Go binary and the agent UI from scratch.
TimeoutStartSec=3600
EOF

# ── Activation ───────────────────────────────────────────────────────────────

# A trigger left over from an interrupted run would fire the moment the path
# unit starts; drop it so installing never kicks off an update. update.sh
# rejects a stale request too, but not creating one is cheaper than refusing it.
rm -f "${SVKEXE_UPDATE_TRIGGER}"

# An image build host or a container has the unit directory but no running
# systemd. Writing the units there is still useful (the image carries them),
# but nothing can be enabled — and the marker must then stay absent, because
# the gateway takes it as proof that a trigger will actually be consumed.
if ! command -v systemctl >/dev/null 2>&1 || [[ ! -d /run/systemd/system ]]; then
    warn "systemd is not running here — units written but not enabled."
    rm -f "${SVKEXE_UPDATE_WATCHER}"
    warn "Removed ${SVKEXE_UPDATE_WATCHER}: this host cannot self-update."
    exit 0
fi

systemctl daemon-reload
if ! systemctl enable --now "${UPDATE_PATH_UNIT}"; then
    warn "Could not enable ${UPDATE_PATH_UNIT} — this host cannot self-update."
    rm -f "${SVKEXE_UPDATE_WATCHER}"
    exit 1
fi

# The marker is the contract with the gateway: Runner.Available() refuses to
# offer an update unless it exists, so it is written only once the watcher is
# demonstrably enabled. Its content names the unit for an operator staring at
# an unexpected file in the data directory.
install -d -m 0750 "$(dirname "${SVKEXE_UPDATE_WATCHER}")"
printf '%s\n' "${UPDATE_PATH_UNIT}" >"${SVKEXE_UPDATE_WATCHER}"
_share_with_service_user "${SVKEXE_UPDATE_WATCHER}"

log "Self-update watcher active: touch ${SVKEXE_UPDATE_TRIGGER} to trigger an update."
