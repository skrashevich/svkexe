#!/usr/bin/env bash
# restore.sh — Restore svkexe gateway database and/or Incus container snapshots.
#
# Usage:
#   restore.sh --db-backup PATH [--force]
#   restore.sh --container NAME --snapshot SNAP [--force]
#   restore.sh --db-backup PATH --container NAME --snapshot SNAP [--force]
#
# Options:
#   --db-backup PATH      Path to the SQLite backup file to restore.
#   --container NAME      Incus container name to restore.
#   --snapshot SNAP       Incus snapshot name to restore.
#   --force               Skip confirmation prompts.

set -euo pipefail

GATEWAY_DB_PATH="${GATEWAY_DB_PATH:-/var/lib/svkexe/gateway.db}"

DB_BACKUP=""
CONTAINER_NAME=""
SNAPSHOT_NAME=""
FORCE=false

LOG_PREFIX="[restore]"
log()   { echo "${LOG_PREFIX} $*"; }
error() { echo "${LOG_PREFIX} ERROR: $*" >&2; }

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
while [[ $# -gt 0 ]]; do
    case "$1" in
        --db-backup)
            DB_BACKUP="$2"; shift 2 ;;
        --container)
            CONTAINER_NAME="$2"; shift 2 ;;
        --snapshot)
            SNAPSHOT_NAME="$2"; shift 2 ;;
        --force)
            FORCE=true; shift ;;
        *)
            error "Unknown argument: $1"
            echo "Usage: $0 [--db-backup PATH] [--container NAME --snapshot SNAP] [--force]" >&2
            exit 1 ;;
    esac
done

if [[ -z "${DB_BACKUP}" && -z "${CONTAINER_NAME}" ]]; then
    error "Nothing to restore. Provide --db-backup and/or --container + --snapshot."
    echo "Usage: $0 [--db-backup PATH] [--container NAME --snapshot SNAP] [--force]" >&2
    exit 1
fi

if [[ -n "${CONTAINER_NAME}" && -z "${SNAPSHOT_NAME}" ]]; then
    error "--snapshot is required when --container is specified."
    exit 1
fi

# ---------------------------------------------------------------------------
# Confirmation helper
# ---------------------------------------------------------------------------
confirm() {
    local msg="$1"
    if [[ "${FORCE}" == true ]]; then
        log "Auto-confirming (--force): ${msg}"
        return 0
    fi
    read -r -p "${LOG_PREFIX} CONFIRM: ${msg} [yes/NO] " answer
    if [[ "${answer}" != "yes" ]]; then
        log "Aborted by user."
        exit 1
    fi
}

# ---------------------------------------------------------------------------
# 1. Database restore
# ---------------------------------------------------------------------------
if [[ -n "${DB_BACKUP}" ]]; then
    if [[ ! -f "${DB_BACKUP}" ]]; then
        error "Backup file not found: ${DB_BACKUP}"
        exit 1
    fi

    confirm "Restore database from '${DB_BACKUP}' to '${GATEWAY_DB_PATH}'? This will OVERWRITE the current database."

    # Stop the gateway service if running (best-effort).
    if command -v systemctl &>/dev/null && systemctl is-active --quiet svkexe-gateway 2>/dev/null; then
        log "Stopping svkexe-gateway service..."
        systemctl stop svkexe-gateway
        RESTART_GATEWAY=true
    else
        RESTART_GATEWAY=false
    fi

    mkdir -p "$(dirname "${GATEWAY_DB_PATH}")"

    # Atomic replace via SQLite backup command into destination.
    log "Restoring database: ${DB_BACKUP} -> ${GATEWAY_DB_PATH}"
    sqlite3 "${DB_BACKUP}" ".backup '${GATEWAY_DB_PATH}'"
    log "Database restore complete."

    if [[ "${RESTART_GATEWAY}" == true ]]; then
        log "Restarting svkexe-gateway service..."
        systemctl start svkexe-gateway
    fi
fi

# ---------------------------------------------------------------------------
# 2. Incus container snapshot restore
# ---------------------------------------------------------------------------
if [[ -n "${CONTAINER_NAME}" ]]; then
    log "Checking container '${CONTAINER_NAME}'..."
    if ! incus info "${CONTAINER_NAME}" &>/dev/null; then
        error "Container not found: ${CONTAINER_NAME}"
        exit 1
    fi

    CONTAINER_STATUS="$(incus list --format csv --columns n,s 2>/dev/null | awk -F, -v name="${CONTAINER_NAME}" '$1 == name {print $2}')"

    confirm "Restore container '${CONTAINER_NAME}' to snapshot '${SNAPSHOT_NAME}'? The container will be stopped and restored."

    if [[ "${CONTAINER_STATUS}" == "RUNNING" ]]; then
        log "Stopping container: ${CONTAINER_NAME}"
        incus stop "${CONTAINER_NAME}"
    fi

    log "Restoring snapshot: ${CONTAINER_NAME}/${SNAPSHOT_NAME}"
    incus restore "${CONTAINER_NAME}" "${SNAPSHOT_NAME}"
    log "Snapshot restore complete."

    if [[ "${CONTAINER_STATUS}" == "RUNNING" ]]; then
        log "Restarting container: ${CONTAINER_NAME}"
        incus start "${CONTAINER_NAME}"
    fi
fi

log "Restore complete."
