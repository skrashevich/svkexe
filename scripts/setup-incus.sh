#!/usr/bin/env bash
# setup-incus.sh — One-time Incus host setup for svkexe.
# Idempotent: safe to re-run. Creates storage pool, network bridge, and profile.
set -euo pipefail

# Detach from any inherited non-tty stdin. `incus` reads YAML config from stdin
# when it's not a terminal, which breaks `storage create` when the script is
# launched from a curl-piped installer.
if [[ ! -t 0 ]]; then
    exec </dev/null
fi

POOL_NAME="svkexe-pool"
POOL_DRIVER="dir"          # Change to "zfs" if ZFS is available on the host
POOL_DIR="/var/lib/incus/storage-pools/${POOL_NAME}"
PROFILE_NAME="svkexe-default"
BRIDGE_NAME="svkexe-br0"
BRIDGE_SUBNET="10.100.0.1/24"

# ── Helpers ─────────────────────────────────────────────────────────────────

log() { echo "[setup-incus] $*"; }

require_root() {
    if [[ "${EUID}" -ne 0 ]]; then
        echo "ERROR: This script must be run as root (or with sudo)." >&2
        exit 1
    fi
}

# ── Require root ─────────────────────────────────────────────────────────────

require_root

# ── Install Incus if missing ──────────────────────────────────────────────────

if ! command -v incus &>/dev/null; then
    log "Incus not found. Installing via snap…"
    if command -v snap &>/dev/null; then
        snap install incus
    else
        # Fallback: use the upstream APT repository (Ubuntu/Debian)
        log "snap not available; trying APT…"
        DISTRO_ID="$(. /etc/os-release && echo "${ID}")"
        DISTRO_CODENAME="$(. /etc/os-release && echo "${VERSION_CODENAME}")"
        curl -fsSL "https://pkgs.zabbly.com/key.asc" \
            | gpg --dearmor -o /etc/apt/keyrings/zabbly.gpg
        cat > /etc/apt/sources.list.d/zabbly-incus-stable.list <<EOF
deb [signed-by=/etc/apt/keyrings/zabbly.gpg] https://pkgs.zabbly.com/incus/stable ${DISTRO_CODENAME} main
EOF
        apt-get update -q
        apt-get install -y incus
    fi
else
    log "Incus already installed: $(incus --version)"
fi

# ── Ensure incus daemon is running ───────────────────────────────────────────

if ! incus info &>/dev/null; then
    log "Starting incus service…"
    systemctl start incus
    sleep 3
fi

# ── Detect ZFS availability ──────────────────────────────────────────────────

if command -v zfs &>/dev/null && zpool list &>/dev/null 2>&1; then
    log "ZFS is available; using ZFS storage driver."
    POOL_DRIVER="zfs"
else
    log "ZFS not available; using 'dir' storage driver."
fi

# ── Create storage pool ──────────────────────────────────────────────────────

if incus storage list --format csv | grep -q "^${POOL_NAME},"; then
    log "Storage pool '${POOL_NAME}' already exists — skipping."
else
    log "Creating storage pool '${POOL_NAME}' (driver: ${POOL_DRIVER})…"
    if [[ "${POOL_DRIVER}" == "zfs" ]]; then
        incus storage create "${POOL_NAME}" zfs
    else
        # Incus refuses to adopt a pre-existing directory. If one is left over
        # from a prior failed run (not registered as a pool), remove it first.
        if [[ -d "${POOL_DIR}" ]]; then
            log "Removing stale pool directory '${POOL_DIR}' from prior run…"
            rm -rf "${POOL_DIR}"
        fi
        # Let Incus create the directory itself under its standard layout —
        # passing source= with a non-existent path is more reliable across
        # versions than pre-creating the dir.
        incus storage create "${POOL_NAME}" dir
    fi
fi

# ── Create network bridge ────────────────────────────────────────────────────

if incus network list --format csv | grep -q "^${BRIDGE_NAME},"; then
    log "Network '${BRIDGE_NAME}' already exists — skipping."
else
    log "Creating network bridge '${BRIDGE_NAME}' (${BRIDGE_SUBNET})…"
    incus network create "${BRIDGE_NAME}" \
        ipv4.address="${BRIDGE_SUBNET}" \
        ipv4.nat=true \
        ipv6.address=none
fi

# ── Create default profile ───────────────────────────────────────────────────

if incus profile list --format csv | grep -q "^${PROFILE_NAME},"; then
    log "Profile '${PROFILE_NAME}' already exists — skipping."
else
    log "Creating profile '${PROFILE_NAME}'…"
    incus profile create "${PROFILE_NAME}"
fi

# Apply resource limits and network/storage config to the profile
log "Configuring profile '${PROFILE_NAME}'…"
incus profile edit "${PROFILE_NAME}" <<EOF
name: ${PROFILE_NAME}
description: svkexe default container profile
config:
  limits.cpu: "2"
  limits.memory: 2GB
  limits.memory.swap: "false"
  security.nesting: "false"
  security.privileged: "false"
devices:
  eth0:
    name: eth0
    network: ${BRIDGE_NAME}
    type: nic
  root:
    path: /
    pool: ${POOL_NAME}
    type: disk
    size: 20GB
  data:
    path: /data
    pool: ${POOL_NAME}
    type: disk
    size: 10GB
EOF

log ""
log "Incus host setup complete."
log "  Storage pool : ${POOL_NAME} (${POOL_DRIVER})"
log "  Network      : ${BRIDGE_NAME} (${BRIDGE_SUBNET})"
log "  Profile      : ${PROFILE_NAME}"
log ""
log "Next step: run scripts/build-image.sh to build the svkexe-base image."
