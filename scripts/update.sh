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
#   SVKEXE_BRANCH         Branch/tag to update to (default: main)
#   SVKEXE_SRC_DIR        Checkout location (default: /opt/svkexe)
#   SVKEXE_UPDATE_STATUS  Machine-readable status file
#                         (default: /var/lib/svkexe/update-status.json)
#   SVKEXE_UPDATE_LOG     Full run log (default: /var/lib/svkexe/update.log)
#   SVKEXE_UPDATE_TRIGGER Request file written by the gateway
#                         (default: /var/lib/svkexe/update.trigger)
#   SVKEXE_UPDATE_MAX_AGE_SECONDS
#                         How old a request may be and still be acted on
#                         (default: 900)
#   SVKEXE_SERVICE_USER   User that must be able to read status/log
#                         (default: svkexe)
#   SKIP_RESTART=1        Build only, don't restart the service
#
# What it does:
#   1. Consumes the update request, if this run was triggered by one.
#   2. Pulls the latest code from the remote.
#   3. Refreshes the self-update systemd units.
#   4. Rebuilds the gateway binary and installs it to
#      /usr/local/bin/svkexe-gateway.
#   5. Restarts the svkexe-gateway systemd service.
#   6. Rebuilds the svkexe-base Incus image when its inputs changed.
#
# The status file is the only channel the web UI has: this script restarts the
# gateway mid-run, so the HTTP request that triggered the update is killed long
# before the update finishes. The gateway polls the status file instead.
#
# Idempotent and safe to re-run.

# -E (errtrace) makes the ERR trap fire for failures inside functions too;
# without it an aborted run inside a helper would never record its context.
set -Eeuo pipefail

log()  { printf '\033[1;34m[update]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[update]\033[0m %s\n' "$*" >&2; }
# die() exits directly, so the ERR trap never sees it — record the reason here
# or the status file would only carry a bare exit code.
die()  { printf '\033[1;31m[update ERROR]\033[0m %s\n' "$*" >&2; FAIL_REASON="${FAIL_REASON:-$*}"; exit 1; }

# ── Status reporting ────────────────────────────────────────────────────────

SVKEXE_UPDATE_STATUS="${SVKEXE_UPDATE_STATUS:-/var/lib/svkexe/update-status.json}"
SVKEXE_UPDATE_LOG="${SVKEXE_UPDATE_LOG:-/var/lib/svkexe/update.log}"
SVKEXE_SERVICE_USER="${SVKEXE_SERVICE_USER:-svkexe}"

STARTED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
COMMIT=""
STATUS_ENABLED=0
LOG_ENABLED=0
LOG_TEEING=0
LOG_TEE_PID=""
STATUS_FINAL=0
FAIL_REASON=""

# Keep pristine copies of stdout/stderr so the log tee can be shut down and
# drained before the status file is written.
exec 3>&1 4>&2

# The gateway runs as an unprivileged user and only reads these files; make
# them group-readable by that user when it exists (it does not on dev boxes).
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

_status_init() {
    local dir
    # Deliberately don't touch the status file here: an empty file would be a
    # parse error for a gateway that polls between init and the first write.
    # The first _write_status creates it atomically.
    dir="$(dirname "${SVKEXE_UPDATE_STATUS}")"
    if mkdir -p "${dir}" 2>/dev/null && [[ -w "${dir}" ]]; then
        STATUS_ENABLED=1
    fi

    [[ "${STATUS_ENABLED}" == "1" ]] \
        || warn "Cannot write ${SVKEXE_UPDATE_STATUS} — the web UI won't see this run."

    # The log is truncated per run: it is a live view of the current update,
    # and the status file carries only a bounded tail of it.
    dir="$(dirname "${SVKEXE_UPDATE_LOG}")"
    # 2>/dev/null comes first: redirections apply left to right, so this also
    # swallows the shell's own "Permission denied" for the truncate below.
    if mkdir -p "${dir}" 2>/dev/null && : 2>/dev/null >"${SVKEXE_UPDATE_LOG}"; then
        LOG_ENABLED=1
        _share_with_service_user "${SVKEXE_UPDATE_LOG}"
    else
        warn "Cannot write ${SVKEXE_UPDATE_LOG} — continuing without a log file."
    fi
}

# Mirror everything to the log file while keeping the coloured console output
# intact for interactive runs.
_log_start() {
    [[ "${LOG_ENABLED}" == "1" ]] || return 0
    exec > >(tee -a "${SVKEXE_UPDATE_LOG}") 2>&1
    LOG_TEEING=1
    # `$!` is unbound under `set -u` on shells that don't report the process
    # substitution PID; an empty value just means we can't wait for the tee.
    set +u; LOG_TEE_PID="$!"; set -u
}

# Restore the original stdout/stderr — that drops the last references to the
# pipe, so tee sees EOF — and wait for tee to exit. Without this the status
# file could be built from a half-flushed log.
_log_flush() {
    [[ "${LOG_TEEING}" == "1" ]] || return 0
    LOG_TEEING=0
    exec 1>&3 2>&4
    [[ -n "${LOG_TEE_PID}" ]] || return 0

    # Bounded wait: a grandchild that inherited the pipe would keep tee alive
    # forever, and hanging here would leave the status file stuck at "running"
    # until systemd's TimeoutStartSec fires. The watchdog is started after the
    # fds are restored, so it doesn't hold the pipe open itself.
    { sleep 5; kill -TERM "${LOG_TEE_PID}" 2>/dev/null; } &
    local watchdog=$!
    wait "${LOG_TEE_PID}" 2>/dev/null || true
    kill -TERM "${watchdog}" 2>/dev/null || true
    wait "${watchdog}" 2>/dev/null || true
    LOG_TEE_PID=""
}

# JSON is built by python3 (already a hard dependency of this script) because
# hand-rolled printf escaping breaks on quotes, backslashes and newlines — and
# the Go side refuses to unmarshal anything malformed.
_write_status_json() {
    local state="$1" finished="$2" error="$3"
    command -v python3 >/dev/null 2>&1 || return 1

    python3 - "${SVKEXE_UPDATE_STATUS}" "${state}" "${STARTED_AT}" \
        "${finished}" "${COMMIT}" "${error}" "${SVKEXE_UPDATE_LOG}" <<'PY'
import json, os, re, sys

path, state, started, finished, commit, error, log_path = sys.argv[1:8]

MAX_BYTES = 8000
MAX_LINES = 200
# Strip the ANSI colours the log helpers emit; the UI renders this as plain text.
ANSI = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")

tail = ""
try:
    with open(log_path, "rb") as fh:
        fh.seek(0, os.SEEK_END)
        size = fh.tell()
        fh.seek(max(0, size - MAX_BYTES))
        chunk = fh.read().decode("utf-8", "replace")
    if size > MAX_BYTES and "\n" in chunk:
        chunk = chunk.split("\n", 1)[1]  # drop the partial first line
    tail = "\n".join(ANSI.sub("", chunk).splitlines()[-MAX_LINES:])
except OSError:
    tail = ""

doc = {
    "state": state,
    "startedAt": started,
    "finishedAt": finished,
    "commit": commit,
    "error": ANSI.sub("", error),
    "log": tail,
}
# Go's time.Time refuses to unmarshal "" — while the run is in flight the key
# must be absent, not empty, or the gateway can't read the status at all.
if not finished:
    del doc["finishedAt"]

tmp = path + ".tmp"
with open(tmp, "w", encoding="utf-8") as fh:
    json.dump(doc, fh)
    fh.write("\n")
os.replace(tmp, path)  # atomic: the gateway may be reading concurrently
PY
}

# Last resort when python3 is missing (a failure before step 1 installs it) or
# unusable: still valid JSON, minus the log tail, with a sanitised message.
_write_status_minimal() {
    local state="$1" finished="$2" error="$3"
    local safe tmp="${SVKEXE_UPDATE_STATUS}.tmp"
    safe="$(printf '%s' "${error}" | tr -d '\\"' | tr '\n\r\t' '   ' | tr -cd '[:print:]')"

    if [[ -n "${finished}" ]]; then
        printf '{"state":"%s","startedAt":"%s","finishedAt":"%s","commit":"%s","error":"%s","log":""}\n' \
            "${state}" "${STARTED_AT}" "${finished}" "${COMMIT}" "${safe}" \
            >"${tmp}" 2>/dev/null || return 1
    else
        # finishedAt is omitted, never "" — see the note in the python path.
        printf '{"state":"%s","startedAt":"%s","commit":"%s","error":"%s","log":""}\n' \
            "${state}" "${STARTED_AT}" "${COMMIT}" "${safe}" \
            >"${tmp}" 2>/dev/null || return 1
    fi
    mv -f "${tmp}" "${SVKEXE_UPDATE_STATUS}" 2>/dev/null || return 1
}

_write_status() {
    local state="$1" error="${2:-}"
    [[ "${STATUS_ENABLED}" == "1" ]] || return 0

    local finished=""
    [[ "${state}" == "running" ]] || finished="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

    # Never let a status write take the script down, but never leave the UI
    # without one either: fall back rather than give up.
    if ! _write_status_json "${state}" "${finished}" "${error}" 2>/dev/null; then
        _write_status_minimal "${state}" "${finished}" "${error}" || true
    fi

    # The atomic replace installs a fresh inode, so re-apply the sharing bits.
    _share_with_service_user "${SVKEXE_UPDATE_STATUS}"
}

_on_err() {
    local code="$1" line="$2" cmd="$3"
    # Only the first failure is interesting; the trap can fire again while the
    # shell unwinds.
    [[ -n "${FAIL_REASON}" ]] || FAIL_REASON="line ${line}: '${cmd}' exited with status ${code}"
}

_on_signal() {
    local sig="$1"
    [[ -n "${FAIL_REASON}" ]] || FAIL_REASON="aborted by SIG${sig}"
    warn "Received SIG${sig} — aborting."
    # Re-raise via a normal exit so the EXIT trap records the failure.
    exit $((128 + $(kill -l "${sig}")))
}

# Any exit path lands here, so an aborted run can never leave the status file
# stuck at "running".
_on_exit() {
    local code="$1"
    if [[ "${STATUS_FINAL}" == "1" ]]; then
        _log_flush
        return 0
    fi
    STATUS_FINAL=1
    local reason="${FAIL_REASON}"
    [[ -n "${reason}" ]] || reason="update aborted with exit code ${code}"
    printf '\033[1;31m[update ERROR]\033[0m Update failed: %s\n' "${reason}" >&2
    _log_flush
    _write_status failed "${reason}"
}

_finish_ok() {
    STATUS_FINAL=1
    _log_flush
    _write_status success ""
}

# A run that deliberately does nothing still owes the UI a terminal status —
# otherwise the state stays at the "running" the gateway seeded when it wrote
# the trigger, and every later update is refused as "already in progress".
# The reason goes in as the error: nothing was updated, and saying "success"
# would claim otherwise.
_finish_ignored() {
    STATUS_FINAL=1
    _log_flush
    _write_status failed "$1"
}

_status_init
_log_start

trap '_on_err "$?" "${LINENO}" "${BASH_COMMAND}"' ERR
trap '_on_signal INT' INT
trap '_on_signal TERM' TERM
trap '_on_exit "$?"' EXIT

_write_status running ""

[[ "${EUID}" -eq 0 ]] || die "Must run as root. Try: sudo $0"

# ── Update request ──────────────────────────────────────────────────────────
#
# The trigger file is how the unprivileged gateway asks for this run; the
# root-owned svkexe-update.path unit watches it with PathExists. Consuming it
# here rather than in the unit's ExecStartPre buys two things: the .path unit
# re-arms as soon as the file is gone, so one click is exactly one run, and we
# get to look at *when* the request was made. A trigger that was never consumed
# — the machine went down mid-update — would otherwise fire on the next boot
# and rebuild the whole platform nobody asked for.
#
# No trigger at all means a manual CLI invocation: always proceed.

SVKEXE_UPDATE_TRIGGER="${SVKEXE_UPDATE_TRIGGER:-/var/lib/svkexe/update.trigger}"
SVKEXE_UPDATE_MAX_AGE_SECONDS="${SVKEXE_UPDATE_MAX_AGE_SECONDS:-900}"

# Prints the request's age in seconds, or fails when the file carries no
# timestamp this script understands. python3 does the parsing for the same
# reason it writes the status file: date arithmetic on an RFC3339 string with a
# fractional part is not something to hand-roll in shell.
_trigger_age_seconds() {
    local file="$1"
    command -v python3 >/dev/null 2>&1 || return 1
    python3 - "${file}" <<'PY'
import datetime, json, re, sys

try:
    with open(sys.argv[1], "r", encoding="utf-8") as fh:
        requested = json.load(fh)["requestedAt"]
except Exception:
    sys.exit(1)

if not isinstance(requested, str):
    sys.exit(1)

text = requested.strip()
# Go emits RFC3339 with a "Z" suffix and up to nanosecond precision;
# fromisoformat only learned both in 3.11, so normalise for older runtimes.
text = re.sub(r"\.(\d{6})\d+", r".\1", text)
if text.endswith(("Z", "z")):
    text = text[:-1] + "+00:00"

try:
    ts = datetime.datetime.fromisoformat(text)
except ValueError:
    sys.exit(1)
if ts.tzinfo is None:
    ts = ts.replace(tzinfo=datetime.timezone.utc)

print(int((datetime.datetime.now(datetime.timezone.utc) - ts).total_seconds()))
PY
}

if [[ -e "${SVKEXE_UPDATE_TRIGGER}" ]]; then
    TRIGGER_AGE="$(_trigger_age_seconds "${SVKEXE_UPDATE_TRIGGER}" 2>/dev/null || true)"
    # Delete before deciding anything: whatever we do with this request, it has
    # been seen, and leaving the file behind would replay it at the next boot.
    rm -f "${SVKEXE_UPDATE_TRIGGER}" \
        || warn "Could not remove ${SVKEXE_UPDATE_TRIGGER} — it may re-trigger."

    if [[ -z "${TRIGGER_AGE}" ]]; then
        REASON="ignored an update request with no readable requestedAt timestamp"
        log "Update request has no usable timestamp — refusing to act on it."
        _finish_ignored "${REASON}"
        exit 0
    elif (( TRIGGER_AGE > SVKEXE_UPDATE_MAX_AGE_SECONDS )); then
        REASON="ignored a stale update request: ${TRIGGER_AGE}s old, limit ${SVKEXE_UPDATE_MAX_AGE_SECONDS}s"
        log "Update request is ${TRIGGER_AGE}s old (limit ${SVKEXE_UPDATE_MAX_AGE_SECONDS}s) — not updating."
        _finish_ignored "${REASON}"
        exit 0
    fi

    log "Acting on an update request made ${TRIGGER_AGE}s ago."
else
    log "No update request file — running as a manual invocation."
fi

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

# All repository access goes through this wrapper.
#
# The checkout is not necessarily owned by root, and git refuses to touch a
# repository owned by someone else ("detected dubious ownership"). Interactively
# that is invisible, because git makes an exception when SUDO_UID says root got
# here through sudo — but svkexe-update.service is started by systemd, where no
# such variable exists, so the self-update would fail at the first fetch.
# Declaring the path safe per-invocation fixes that without mutating root's
# global git configuration.
git_repo() { git -c safe.directory="${REPO_ROOT}" -C "${REPO_ROOT}" "$@"; }

# ── Constants ───────────────────────────────────────────────────────────────

BIN_NAME="svkexe-gateway"
INSTALL_PREFIX="/usr/local"
GO_INSTALL_DIR="/usr/local/go"
SERVICE_NAME="${BIN_NAME}.service"

export PATH="${GO_INSTALL_DIR}/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

# ── Step 1: Pull latest code ───────────────────────────────────────────────

OLD_COMMIT=""
if [[ -d "${REPO_ROOT}/.git" ]]; then
    OLD_COMMIT="$(git_repo rev-parse HEAD 2>/dev/null || true)"
    log "Pulling latest changes (branch: ${SVKEXE_BRANCH})…"
    # --tags: the Makefile stamps the binary with `git describe --tags`, and the
    # release-channel update check compares that string against the latest
    # release. Fetching without tags leaves it a bare SHA, which never matches
    # and makes the gateway advertise an update it just installed.
    git_repo fetch --tags origin "${SVKEXE_BRANCH}"
    git_repo checkout -q "${SVKEXE_BRANCH}"
    git_repo reset --hard "origin/${SVKEXE_BRANCH}"
    COMMIT="$(git_repo rev-parse --short HEAD)"
    log "Updated to commit ${COMMIT}."
else
    warn "${REPO_ROOT} is not a git repo — skipping pull, building from current state."
fi

# ── Step 1b: Refresh the self-update units ──────────────────────────────────
#
# The units are versioned with the code, so refreshing them here is what lets a
# fix to the update plumbing reach a host that only ever runs update.sh. Never
# fatal: a host that cannot install units can still take the new binary, and
# install-update-units.sh drops the watcher marker in that case so the gateway
# stops offering a button nothing would answer.

UNITS_SCRIPT="${REPO_ROOT}/scripts/install-update-units.sh"
if [[ -f "${UNITS_SCRIPT}" ]]; then
    log "Refreshing self-update systemd units…"
    env \
        SVKEXE_SRC_DIR="${REPO_ROOT}" \
        SVKEXE_BRANCH="${SVKEXE_BRANCH}" \
        DATA_DIR="$(dirname "${SVKEXE_UPDATE_STATUS}")" \
        SVKEXE_UPDATE_TRIGGER="${SVKEXE_UPDATE_TRIGGER}" \
        SVKEXE_UPDATE_STATUS="${SVKEXE_UPDATE_STATUS}" \
        SVKEXE_UPDATE_LOG="${SVKEXE_UPDATE_LOG}" \
        SVKEXE_SERVICE_USER="${SVKEXE_SERVICE_USER}" \
        bash "${UNITS_SCRIPT}" \
        || warn "Could not refresh the self-update units — continuing with the update."
else
    warn "${UNITS_SCRIPT} not found — self-update units left as they are."
fi

# ── Step 1c: Refresh the instance metadata units ────────────────────────────
#
# These carry the redirect that puts a VM's request to 169.254.169.254 in front
# of the gateway, and the NIC filtering that makes a VM's source address
# trustworthy enough to answer at all. Refreshing them here is what lets the
# feature reach a host that only ever runs update.sh — without it the gateway
# comes back up refusing every VM's metadata request.

METADATA_UNITS_SCRIPT="${REPO_ROOT}/scripts/install-metadata-units.sh"
if [[ -f "${METADATA_UNITS_SCRIPT}" ]]; then
    log "Refreshing instance metadata units…"
    bash "${METADATA_UNITS_SCRIPT}" \
        || warn "Could not refresh the instance metadata units — continuing with the update."
else
    warn "${METADATA_UNITS_SCRIPT} not found — instance metadata units left as they are."
fi

if ! command -v node >/dev/null || ! command -v npm >/dev/null || ! command -v python3 >/dev/null; then
    apt-get update -q
    apt-get install -y nodejs npm python3
fi

# ── Step 2: Build ───────────────────────────────────────────────────────────

command -v go &>/dev/null || die "Go not found. Install it or run the full installer first."

log "Building gateway binary…"
env HOME="/root" make -C "${REPO_ROOT}" build

# The status file reports what was actually built, so re-read HEAD here: a
# non-git checkout never set COMMIT above.
if [[ -d "${REPO_ROOT}/.git" ]]; then
    COMMIT="$(git_repo rev-parse --short HEAD 2>/dev/null || true)"
fi

# ── Step 3: Install binary ──────────────────────────────────────────────────

log "Installing binary to ${INSTALL_PREFIX}/bin/${BIN_NAME}…"
install -m 0755 "${REPO_ROOT}/bin/gateway" "${INSTALL_PREFIX}/bin/${BIN_NAME}"
install -d -m 0755 "${INSTALL_PREFIX}/lib/svkexe"
install -m 0755 "${REPO_ROOT}/bin/picoclaw" "${INSTALL_PREFIX}/lib/svkexe/picoclaw"
install -d -m 0755 "${INSTALL_PREFIX}/share/licenses/svkexe-agent"
install -m 0644 "${REPO_ROOT}"/agent/licenses/* "${INSTALL_PREFIX}/share/licenses/svkexe-agent/"

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

# ── Step 5: Rebuild base image if its inputs changed ────────────────────────
#
# Deliberately last. scripts/build-image.sh deletes the svkexe-base alias before
# republishing it, so a failure halfway through (apt, npm, the network) leaves
# the host unable to create VMs. Running it after the binary is installed and
# the service is back up bounds the damage to the image: one button click can
# cost the operator the image rebuild, or the gateway update, but not both.

if [[ -d "${REPO_ROOT}/.git" ]]; then
    if [[ -z "${OLD_COMMIT}" ]]; then
        log "No pre-pull commit recorded — skipping base image rebuild check."
    else
        NEW_COMMIT="$(git_repo rev-parse HEAD)"
        if [[ "${OLD_COMMIT}" == "${NEW_COMMIT}" ]]; then
            log "No new commits — skipping base image rebuild check."
        elif git_repo diff --name-only "${OLD_COMMIT}" "${NEW_COMMIT}" \
             | grep -qE '^(scripts/build-(image|agent)\.sh|agent/)'; then
            log "Base image inputs changed (build-image.sh, build-agent.sh or agent/) — rebuilding svkexe-base…"
            # The agent was just built above; hand it over instead of building it
            # again. HOME is set for the same reason as the gateway build: under
            # systemd-run the image build has no home and go cannot locate its
            # module cache.
            env HOME="/root" SVKEXE_AGENT_BINARY="${REPO_ROOT}/bin/picoclaw" \
                "${BASH}" "${REPO_ROOT}/scripts/build-image.sh"
        else
            log "Base image inputs unchanged — skipping base image rebuild."
        fi
    fi
else
    log "No git history available — skipping base image rebuild check."
fi

# ── Summary ─────────────────────────────────────────────────────────────────

log "Update complete. Binary: ${INSTALL_PREFIX}/bin/${BIN_NAME}"

_finish_ok
