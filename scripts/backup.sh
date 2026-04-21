#!/usr/bin/env bash
# backup.sh — Automated backup for svkexe gateway.
# Backs up the SQLite gateway database and creates Incus snapshots for all
# running containers. Retains backups for 7 days. Exits non-zero on failure.
# Suitable for use in cron.
#
# Environment variables (override defaults):
#   GATEWAY_DB_PATH   Path to the SQLite database (default: /var/lib/svkexe/gateway.db)
#   BACKUP_DIR        Destination directory for backups (default: /var/backups/svkexe)
#   RETENTION_DAYS    Days to retain backups (default: 7)

set -euo pipefail

GATEWAY_DB_PATH="${GATEWAY_DB_PATH:-/var/lib/svkexe/gateway.db}"
BACKUP_DIR="${BACKUP_DIR:-/var/backups/svkexe}"
RETENTION_DAYS="${RETENTION_DAYS:-7}"

TIMESTAMP="$(date +%Y%m%dT%H%M%S)"
LOG_PREFIX="[backup ${TIMESTAMP}]"

log() { echo "${LOG_PREFIX} $*"; }
error() { echo "${LOG_PREFIX} ERROR: $*" >&2; }

# ---------------------------------------------------------------------------
# 1. Prepare backup directory
# ---------------------------------------------------------------------------
mkdir -p "${BACKUP_DIR}/db" "${BACKUP_DIR}/snapshots"

# ---------------------------------------------------------------------------
# 2. SQLite online backup
# ---------------------------------------------------------------------------
DB_BACKUP="${BACKUP_DIR}/db/gateway-${TIMESTAMP}.db"
log "Backing up database ${GATEWAY_DB_PATH} -> ${DB_BACKUP}"

if [[ ! -f "${GATEWAY_DB_PATH}" ]]; then
    error "Database not found: ${GATEWAY_DB_PATH}"
    exit 1
fi

sqlite3 "${GATEWAY_DB_PATH}" ".backup '${DB_BACKUP}'"
log "Database backup complete: ${DB_BACKUP}"

# ---------------------------------------------------------------------------
# 3. Incus container snapshots
# ---------------------------------------------------------------------------
SNAPSHOT_NAME="backup-${TIMESTAMP}"
log "Creating Incus snapshots with name: ${SNAPSHOT_NAME}"

CONTAINERS="$(incus list --format csv --columns n,s 2>/dev/null | awk -F, '$2 == "RUNNING" {print $1}')"

if [[ -z "${CONTAINERS}" ]]; then
    log "No running containers found — skipping snapshots."
else
    SNAPSHOT_ERRORS=0
    while IFS= read -r container; do
        log "  Snapshotting container: ${container}"
        if incus snapshot "${container}" "${SNAPSHOT_NAME}" 2>&1; then
            log "  Snapshot created: ${container}/${SNAPSHOT_NAME}"
            # Record snapshot reference for pruning
            echo "${container}:${SNAPSHOT_NAME}" >> "${BACKUP_DIR}/snapshots/manifest-${TIMESTAMP}.txt"
        else
            error "  Failed to snapshot container: ${container}"
            SNAPSHOT_ERRORS=$((SNAPSHOT_ERRORS + 1))
        fi
    done <<< "${CONTAINERS}"

    if [[ "${SNAPSHOT_ERRORS}" -gt 0 ]]; then
        error "Snapshot errors: ${SNAPSHOT_ERRORS} container(s) failed."
        exit 1
    fi
fi

# ---------------------------------------------------------------------------
# 4. Retention — remove backups older than RETENTION_DAYS
# ---------------------------------------------------------------------------
log "Pruning database backups older than ${RETENTION_DAYS} days..."
find "${BACKUP_DIR}/db" -name "gateway-*.db" -mtime "+${RETENTION_DAYS}" -delete

log "Pruning old Incus snapshots from manifest files older than ${RETENTION_DAYS} days..."
find "${BACKUP_DIR}/snapshots" -name "manifest-*.txt" -mtime "+${RETENTION_DAYS}" | while IFS= read -r manifest; do
    while IFS= read -r entry; do
        container="${entry%%:*}"
        snapshot="${entry##*:}"
        log "  Deleting snapshot ${container}/${snapshot}"
        incus delete "${container}/${snapshot}" 2>&1 || true
    done < "${manifest}"
    rm -f "${manifest}"
done

log "Backup complete."
