#!/usr/bin/env bash
# install.sh — End-to-end installer for svkexe on Ubuntu 24.04+ (and Debian 12+).
#
# One-liner installation (on the target server):
#
#   curl -fsSL https://raw.githubusercontent.com/skrashevich/svkexe/main/scripts/install.sh | sudo bash
#
# or, from a local checkout:
#
#   sudo ./scripts/install.sh
#
# Environment overrides (both modes):
#   SVKEXE_REPO         Git repo URL (default: https://github.com/skrashevich/svkexe)
#   SVKEXE_BRANCH       Branch/tag to check out (default: main)
#   SVKEXE_SRC_DIR      Clone target when running piped (default: /opt/svkexe)
#   GO_VERSION          Go version (default: parsed from go.mod, fallback 1.23.4)
#   DOMAIN              Base domain written to /etc/svkexe/gateway.env
#   ACME_EMAIL          ACME email for Caddy
#   GATEWAY_ENC_KEY     Pre-existing hex key; generated if empty
#   SKIP_DOCKER=1       Skip Docker / Compose install
#   SKIP_INCUS=1        Skip Incus install and host setup
#   SKIP_IMAGE_BUILD=1  Skip `svkexe-base` Incus image build (long step)
#   SKIP_GO=1           Skip Go install (assume already present)
#   SKIP_BUILD=1        Skip gateway binary build
#   SKIP_SERVICE=1      Skip systemd unit install
#
# What it does:
#   1. Verifies OS (Ubuntu 24.04+ / Debian 12+), arch (amd64/arm64), root.
#   2. If not run from a checkout, clones the repo to ${SVKEXE_SRC_DIR}.
#   3. Installs base packages, Go, Docker + Compose, Incus.
#   4. Runs scripts/setup-incus.sh and scripts/build-image.sh.
#   5. Builds the gateway binary, installs /usr/local/bin/svkexe-gateway.
#   6. Creates svkexe user, /var/lib/svkexe, /etc/svkexe/gateway.env.
#   7. Installs and enables the svkexe-gateway.service systemd unit.
#
# Idempotent: re-running is safe; completed steps are skipped.

set -euo pipefail

# ── Output helpers ───────────────────────────────────────────────────────────

log()  { printf '\033[1;34m[install]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[install]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[install ERROR]\033[0m %s\n' "$*" >&2; exit 1; }

# ── Preflight: root + OS + arch ──────────────────────────────────────────────

[[ "${EUID}" -eq 0 ]] || die "Must run as root. Try: curl -fsSL … | sudo bash"

[[ -f /etc/os-release ]] || die "/etc/os-release missing — unsupported OS."
# shellcheck disable=SC1091
. /etc/os-release

case "${ID:-}:${VERSION_ID:-}" in
    ubuntu:24.04|ubuntu:24.10|ubuntu:25.04|ubuntu:25.10|ubuntu:26.04)
        log "Detected ${PRETTY_NAME}." ;;
    debian:12|debian:13)
        log "Detected ${PRETTY_NAME} (supported)." ;;
    ubuntu:*|debian:*)
        warn "Detected ${PRETTY_NAME} — not explicitly tested, proceeding anyway." ;;
    *)
        die "Unsupported distribution '${ID:-unknown} ${VERSION_ID:-}'. Requires Ubuntu 24.04+ or Debian 12+." ;;
esac

ARCH="$(dpkg --print-architecture)"
case "${ARCH}" in
    amd64|arm64) log "Architecture: ${ARCH}." ;;
    *) die "Unsupported architecture '${ARCH}'. Only amd64 and arm64 are supported." ;;
esac

TARGET_USER="${SUDO_USER:-root}"
log "Primary operator user: ${TARGET_USER}"

export DEBIAN_FRONTEND=noninteractive

# ── Bootstrap: detect pipe-install, clone if needed ─────────────────────────
#
# When invoked via `curl | bash`, BASH_SOURCE[0] is empty or points at a path
# that doesn't exist on disk. In that case we clone the repo to SVKEXE_SRC_DIR
# and re-exec this script from there. When invoked from an existing checkout,
# we just use it in place.

SVKEXE_REPO="${SVKEXE_REPO:-https://github.com/skrashevich/svkexe}"
SVKEXE_BRANCH="${SVKEXE_BRANCH:-main}"
SVKEXE_SRC_DIR="${SVKEXE_SRC_DIR:-/opt/svkexe}"

_raw_script="${BASH_SOURCE[0]:-}"
if [[ -n "${_raw_script}" && -f "${_raw_script}" ]]; then
    SCRIPT_DIR="$(cd "$(dirname "${_raw_script}")" && pwd)"
    CANDIDATE_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
else
    SCRIPT_DIR=""
    CANDIDATE_ROOT=""
fi

if [[ -n "${CANDIDATE_ROOT}" && -f "${CANDIDATE_ROOT}/go.mod" && -x "${SCRIPT_DIR}/setup-incus.sh" ]]; then
    log "Running from local checkout: ${CANDIDATE_ROOT}"
    REPO_ROOT="${CANDIDATE_ROOT}"
else
    log "No local checkout detected — bootstrapping from ${SVKEXE_REPO} (branch ${SVKEXE_BRANCH})."

    if ! command -v git &>/dev/null || ! command -v curl &>/dev/null; then
        log "Installing bootstrap prerequisites (git, curl, ca-certificates)…"
        apt-get update -q
        apt-get install -y --no-install-recommends git curl ca-certificates
    fi

    mkdir -p "$(dirname "${SVKEXE_SRC_DIR}")"
    if [[ -d "${SVKEXE_SRC_DIR}/.git" ]]; then
        log "Updating existing clone at ${SVKEXE_SRC_DIR}…"
        git -C "${SVKEXE_SRC_DIR}" fetch --depth 1 origin "${SVKEXE_BRANCH}"
        git -C "${SVKEXE_SRC_DIR}" checkout -q "${SVKEXE_BRANCH}"
        git -C "${SVKEXE_SRC_DIR}" reset --hard "origin/${SVKEXE_BRANCH}"
    else
        log "Cloning ${SVKEXE_REPO} → ${SVKEXE_SRC_DIR}…"
        git clone --depth 1 --branch "${SVKEXE_BRANCH}" "${SVKEXE_REPO}.git" "${SVKEXE_SRC_DIR}"
    fi

    REPO_ROOT="${SVKEXE_SRC_DIR}"
    SCRIPT_DIR="${SVKEXE_SRC_DIR}/scripts"

    # Make sure the freshly-cloned helper scripts are executable.
    chmod +x "${SCRIPT_DIR}"/*.sh 2>/dev/null || true

    log "Re-executing installer from ${SCRIPT_DIR}/install.sh…"
    # Detach stdin from the curl pipe — helper tools (Incus, apt, etc.)
    # otherwise read leftover script bytes and misinterpret them as YAML config.
    exec </dev/null env \
        SVKEXE_REPO="${SVKEXE_REPO}" \
        SVKEXE_BRANCH="${SVKEXE_BRANCH}" \
        SVKEXE_SRC_DIR="${SVKEXE_SRC_DIR}" \
        _SVKEXE_BOOTSTRAPPED=1 \
        bash "${SCRIPT_DIR}/install.sh" "$@"
fi

# Defensive: if we reached here directly (local checkout) and stdin is a pipe,
# detach it so subprocesses don't inherit noise.
if [[ ! -t 0 ]]; then
    exec </dev/null
fi

# ── Paths and constants ──────────────────────────────────────────────────────

INSTALL_PREFIX="/usr/local"
BIN_NAME="svkexe-gateway"
DATA_DIR="/var/lib/svkexe"
CONF_DIR="/etc/svkexe"
ENV_FILE="${CONF_DIR}/gateway.env"
SERVICE_FILE="/etc/systemd/system/${BIN_NAME}.service"
GO_FALLBACK_VERSION="1.23.4"
GO_INSTALL_DIR="/usr/local/go"

# ── Detect Go version from go.mod ────────────────────────────────────────────

detect_go_version() {
    local declared
    declared="$(awk '/^go [0-9]/ {print $2; exit}' "${REPO_ROOT}/go.mod" 2>/dev/null || true)"
    if [[ -z "${declared}" ]]; then
        echo "${GO_FALLBACK_VERSION}"
    else
        echo "${declared}"
    fi
}

GO_VERSION="${GO_VERSION:-$(detect_go_version)}"

# ── Step 1: base system packages ─────────────────────────────────────────────

log "Updating apt index…"
apt-get update -q

log "Installing base packages…"
apt-get install -y --no-install-recommends \
    build-essential \
    ca-certificates \
    curl \
    git \
    gnupg \
    lsb-release \
    openssl \
    sqlite3 \
    jq \
    make \
    pkg-config \
    uidmap \
    rsync

install -d -m 0755 /etc/apt/keyrings

# ── Step 2: Go toolchain ─────────────────────────────────────────────────────

install_go() {
    local want="$1"
    local current=""
    if [[ -x "${GO_INSTALL_DIR}/bin/go" ]]; then
        current="$("${GO_INSTALL_DIR}/bin/go" version 2>/dev/null | awk '{print $3}' | sed 's/^go//')"
    fi
    if [[ "${current}" == "${want}" ]]; then
        log "Go ${want} already installed at ${GO_INSTALL_DIR}."
        return 0
    fi

    local url="https://go.dev/dl/go${want}.linux-${ARCH}.tar.gz"
    log "Downloading Go ${want} (${ARCH})…"
    if ! curl -fsSL -o /tmp/go.tar.gz "${url}"; then
        warn "Go ${want} not published yet at go.dev; falling back to ${GO_FALLBACK_VERSION}."
        want="${GO_FALLBACK_VERSION}"
        url="https://go.dev/dl/go${want}.linux-${ARCH}.tar.gz"
        curl -fsSL -o /tmp/go.tar.gz "${url}" || die "Failed to download Go ${want}."
    fi

    log "Installing Go ${want} to ${GO_INSTALL_DIR}…"
    rm -rf "${GO_INSTALL_DIR}"
    tar -C /usr/local -xzf /tmp/go.tar.gz
    rm -f /tmp/go.tar.gz

    cat >/etc/profile.d/go.sh <<'EOF'
export PATH="$PATH:/usr/local/go/bin"
export GOPATH="${GOPATH:-$HOME/go}"
EOF
    chmod 0644 /etc/profile.d/go.sh
}

if [[ "${SKIP_GO:-0}" != "1" ]]; then
    install_go "${GO_VERSION}"
else
    log "SKIP_GO=1 — skipping Go install."
fi

export PATH="${GO_INSTALL_DIR}/bin:${PATH}"

# ── Step 3: Docker Engine + Compose plugin ───────────────────────────────────

install_docker() {
    if command -v docker &>/dev/null && docker compose version &>/dev/null; then
        log "Docker + Compose already installed: $(docker --version)"
        return 0
    fi

    log "Installing Docker Engine via official Docker APT repository…"

    local dist_id="${ID}"
    local codename="${VERSION_CODENAME:-bookworm}"

    curl -fsSL "https://download.docker.com/linux/${dist_id}/gpg" \
        | gpg --dearmor --yes -o /etc/apt/keyrings/docker.gpg
    chmod 0644 /etc/apt/keyrings/docker.gpg

    cat >/etc/apt/sources.list.d/docker.list <<EOF
deb [arch=${ARCH} signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/${dist_id} ${codename} stable
EOF

    apt-get update -q
    apt-get install -y --no-install-recommends \
        docker-ce \
        docker-ce-cli \
        containerd.io \
        docker-buildx-plugin \
        docker-compose-plugin

    systemctl enable --now docker

    if [[ "${TARGET_USER}" != "root" ]]; then
        usermod -aG docker "${TARGET_USER}" || true
        log "Added ${TARGET_USER} to the 'docker' group (re-login to take effect)."
    fi
}

if [[ "${SKIP_DOCKER:-0}" != "1" ]]; then
    install_docker
else
    log "SKIP_DOCKER=1 — skipping Docker install."
fi

# ── Step 4: Incus ────────────────────────────────────────────────────────────

install_incus() {
    if command -v incus &>/dev/null; then
        log "Incus already installed: $(incus --version 2>&1 | head -n1)"
        return 0
    fi

    log "Installing Incus via Zabbly stable APT repository…"
    local codename="${VERSION_CODENAME:-bookworm}"

    curl -fsSL https://pkgs.zabbly.com/key.asc \
        | gpg --dearmor --yes -o /etc/apt/keyrings/zabbly.gpg
    chmod 0644 /etc/apt/keyrings/zabbly.gpg

    cat >/etc/apt/sources.list.d/zabbly-incus-stable.sources <<EOF
Enabled: yes
Types: deb
URIs: https://pkgs.zabbly.com/incus/stable
Suites: ${codename}
Components: main
Architectures: ${ARCH}
Signed-By: /etc/apt/keyrings/zabbly.gpg
EOF

    apt-get update -q
    apt-get install -y --no-install-recommends incus incus-client

    systemctl enable --now incus
    for _ in {1..10}; do incus info &>/dev/null && break; sleep 1; done

    if [[ "${TARGET_USER}" != "root" ]]; then
        usermod -aG incus-admin "${TARGET_USER}" || true
        log "Added ${TARGET_USER} to 'incus-admin' group (re-login to take effect)."
    fi
}

if [[ "${SKIP_INCUS:-0}" != "1" ]]; then
    install_incus
else
    log "SKIP_INCUS=1 — skipping Incus install."
fi

# ── Step 5: Incus host setup ─────────────────────────────────────────────────

if [[ "${SKIP_INCUS:-0}" != "1" && -x "${SCRIPT_DIR}/setup-incus.sh" ]]; then
    log "Running scripts/setup-incus.sh…"
    "${SCRIPT_DIR}/setup-incus.sh"
fi

# ── Step 5b: Docker ↔ Incus firewall integration ────────────────────────────
#
# Docker installs iptables rules with FORWARD policy DROP and a DOCKER-USER
# chain consulted first. Without explicit allow rules, traffic from the Incus
# bridge (svkexe-br0) to the outside world is silently dropped — containers
# can resolve DNS but time out on every TCP connection.
#
# Install a systemd oneshot that inserts accept rules on boot and is idempotent
# on re-run.

if [[ "${SKIP_INCUS:-0}" != "1" && "${SKIP_DOCKER:-0}" != "1" ]] \
        && command -v iptables &>/dev/null \
        && iptables -nL DOCKER-USER &>/dev/null; then
    log "Installing Docker↔Incus firewall bridge rules…"

    sysctl -w net.ipv4.ip_forward=1 >/dev/null
    cat >/etc/sysctl.d/99-svkexe-forward.conf <<'EOF'
net.ipv4.ip_forward = 1
EOF

    cat >/etc/systemd/system/svkexe-firewall.service <<'EOF'
[Unit]
Description=svkexe firewall integration (allow Incus bridge through Docker FORWARD)
After=docker.service incus.service
Wants=docker.service incus.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/bash -c 'iptables -C DOCKER-USER -i svkexe-br0 -j ACCEPT 2>/dev/null || iptables -I DOCKER-USER -i svkexe-br0 -j ACCEPT'
ExecStart=/bin/bash -c 'iptables -C DOCKER-USER -o svkexe-br0 -j ACCEPT 2>/dev/null || iptables -I DOCKER-USER -o svkexe-br0 -j ACCEPT'
ExecStop=/bin/bash -c 'iptables -D DOCKER-USER -i svkexe-br0 -j ACCEPT 2>/dev/null || true'
ExecStop=/bin/bash -c 'iptables -D DOCKER-USER -o svkexe-br0 -j ACCEPT 2>/dev/null || true'

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable --now svkexe-firewall.service
    log "svkexe-firewall.service enabled and active."
fi

# ── Step 6: Build svkexe-base Incus image ────────────────────────────────────

if [[ "${SKIP_INCUS:-0}" != "1" && "${SKIP_IMAGE_BUILD:-0}" != "1" && -x "${SCRIPT_DIR}/build-image.sh" ]]; then
    if incus image list --format csv 2>/dev/null | grep -q "^svkexe-base,"; then
        log "Incus image 'svkexe-base' already exists — skipping. Delete it and re-run to rebuild."
    else
        log "Running scripts/build-image.sh (this can take several minutes)…"
        "${SCRIPT_DIR}/build-image.sh"
    fi
else
    log "Skipping svkexe-base image build."
fi

# ── Step 7: Build the gateway binary ─────────────────────────────────────────

if [[ "${SKIP_BUILD:-0}" != "1" ]]; then
    log "Building gateway binary…"
    # Run as root with explicit PATH — sudo -u stripping user env was breaking
    # `make` resolution. Module cache goes under /root/go; fine for a one-shot
    # server install.
    env \
        PATH="${GO_INSTALL_DIR}/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" \
        HOME="/root" \
        make -C "${REPO_ROOT}" build

    log "Installing binary to ${INSTALL_PREFIX}/bin/${BIN_NAME}…"
    install -m 0755 "${REPO_ROOT}/bin/gateway" "${INSTALL_PREFIX}/bin/${BIN_NAME}"

    if [[ "${TARGET_USER}" != "root" ]] && id "${TARGET_USER}" &>/dev/null; then
        # Let the operator's user own the checkout so future manual builds work.
        chown -R "${TARGET_USER}":"${TARGET_USER}" "${REPO_ROOT}" 2>/dev/null || true
    fi
else
    log "SKIP_BUILD=1 — skipping gateway build."
fi

# ── Step 8: Data and config directories + service user ──────────────────────

log "Creating ${DATA_DIR} and ${CONF_DIR}…"
install -d -m 0750 "${DATA_DIR}"
install -d -m 0750 "${CONF_DIR}"

if ! id svkexe &>/dev/null; then
    log "Creating system user 'svkexe'…"
    useradd --system --home-dir "${DATA_DIR}" --shell /usr/sbin/nologin svkexe
fi
chown -R svkexe:svkexe "${DATA_DIR}"
chown root:svkexe "${CONF_DIR}"

if getent group incus-admin &>/dev/null; then
    usermod -aG incus-admin svkexe || true
fi

# ── Step 9: gateway.env seed ─────────────────────────────────────────────────

if [[ ! -f "${ENV_FILE}" ]]; then
    GATEWAY_ENC_KEY="${GATEWAY_ENC_KEY:-$(openssl rand -hex 32)}"
    BOOTSTRAP_ADMIN_EMAIL="${BOOTSTRAP_ADMIN_EMAIL:-admin@${DOMAIN:-localhost}}"
    BOOTSTRAP_ADMIN_PASSWORD="${BOOTSTRAP_ADMIN_PASSWORD:-$(openssl rand -base64 18 | tr -d '/+=' | head -c 24)}"
    log "Seeding ${ENV_FILE} (edit it before starting the service)…"
    cat >"${ENV_FILE}" <<EOF
# svkexe-gateway runtime environment — edit before starting the service.
# Generated on $(date -Iseconds) by scripts/install.sh.

GATEWAY_ADDR=:8080
GATEWAY_DB_PATH=${DATA_DIR}/gateway.db
GATEWAY_ENC_KEY=${GATEWAY_ENC_KEY}

# Base domain for subdomain routing (e.g. example.com). Required in production.
DOMAIN=${DOMAIN:-}

# ACME email used by Caddy if you deploy the optional docker compose stack.
ACME_EMAIL=${ACME_EMAIL:-}

# Built-in authentication — first admin created automatically at startup. The
# password is re-applied on every restart, so rotate it here to reset access.
# Set GATEWAY_COOKIE_SECURE=1 when the gateway is served over HTTPS.
BOOTSTRAP_ADMIN_EMAIL=${BOOTSTRAP_ADMIN_EMAIL}
BOOTSTRAP_ADMIN_PASSWORD=${BOOTSTRAP_ADMIN_PASSWORD}
GATEWAY_COOKIE_SECURE=0

# Incus API socket.
INCUS_SOCKET=/var/lib/incus/unix.socket

# Secrets, SSH, rate limiting.
SECRETS_BASE_PATH=${DATA_DIR}/secrets
SSH_ADDR=:2222
SSH_HOST_KEY_PATH=${DATA_DIR}/ssh_host_key
RATE_LIMIT_RPS=10
RATE_LIMIT_BURST=20

# LLM reverse proxy — enables Shelley to use OpenRouter through the gateway.
# Set OPENROUTER_API_KEY to enable. Models are tried in order until one works.
OPENROUTER_API_KEY=
OPENROUTER_MODELS=anthropic/claude-sonnet-4,openai/gpt-4o,google/gemini-2.5-flash
LLM_INTERNAL_TOKEN=$(openssl rand -hex 16)
# LLM_PROXY_URL=https://\${DOMAIN}/api/llm/v1  # auto-derived from DOMAIN
EOF
    chown root:svkexe "${ENV_FILE}"
    chmod 0640 "${ENV_FILE}"
    # Print the generated admin password so the operator can grab it from
    # stdout; it is also inside the (root-readable) env file.
    log "Initial admin account: ${BOOTSTRAP_ADMIN_EMAIL}"
    log "Initial admin password: ${BOOTSTRAP_ADMIN_PASSWORD}"
    log "Login at http://<host>:8080/login — rotate BOOTSTRAP_ADMIN_PASSWORD in ${ENV_FILE} to change."
else
    log "${ENV_FILE} exists — leaving untouched."
fi

# ── Step 10: systemd unit ────────────────────────────────────────────────────

if [[ "${SKIP_SERVICE:-0}" != "1" ]]; then
    log "Installing systemd unit ${SERVICE_FILE}…"
    cat >"${SERVICE_FILE}" <<EOF
[Unit]
Description=svkexe API gateway
Documentation=https://github.com/skrashevich/svkexe
After=network-online.target incus.service
Wants=network-online.target

[Service]
Type=simple
User=svkexe
Group=svkexe
EnvironmentFile=${ENV_FILE}
ExecStart=${INSTALL_PREFIX}/bin/${BIN_NAME}
Restart=on-failure
RestartSec=5
LimitNOFILE=65536

# /run/shelley is managed by systemd: created on start, cleaned on stop,
# owned by the service user. Avoids the 226/NAMESPACE failure that happens
# when ReadWritePaths references a non-existent directory.
RuntimeDirectory=shelley
RuntimeDirectoryMode=0750

# Hardening
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=${DATA_DIR}
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
    systemctl enable "${BIN_NAME}.service"
    log "Unit enabled. Start it with: sudo systemctl start ${BIN_NAME}"
else
    log "SKIP_SERVICE=1 — skipping systemd unit install."
fi

# ── Summary ──────────────────────────────────────────────────────────────────

cat <<EOF

────────────────────────────────────────────────────────────────
 svkexe install complete.

 Source     : ${REPO_ROOT}
 Binary     : ${INSTALL_PREFIX}/bin/${BIN_NAME}
 Data dir   : ${DATA_DIR}
 Config     : ${ENV_FILE}
 Service    : ${BIN_NAME}.service (enabled, not started)

 Next steps:
   1. Review and edit: sudo \$EDITOR ${ENV_FILE}
      - set DOMAIN and ACME_EMAIL for production
   2. (Optional) Deploy Caddy + Authelia + Prometheus/Grafana:
        cd ${REPO_ROOT}/deploy && docker compose up -d
   3. Start the gateway:
        sudo systemctl start ${BIN_NAME}
        sudo systemctl status ${BIN_NAME}
        journalctl -u ${BIN_NAME} -f

 Group changes (docker / incus-admin) require re-login to apply.
────────────────────────────────────────────────────────────────
EOF
