#!/usr/bin/env bash
# build-image.sh — Build Incus base container image 'svkexe-base' with Shelley installed.
# Idempotent: safe to re-run. Cleans up the working container on exit.
set -euo pipefail

# Detach from any inherited non-tty stdin so `incus` doesn't try to parse it as
# YAML config when launched from a piped installer.
if [[ ! -t 0 ]]; then
    exec </dev/null
fi

IMAGE_NAME="svkexe-base"
CONTAINER_NAME="svkexe-build-$$"

# Versions / config
GO_VERSION="1.22.4"
NODE_MAJOR="20"

# ── Helpers ─────────────────────────────────────────────────────────────────

log() { echo "[build-image] $*"; }

cleanup() {
    log "Cleaning up working container…"
    incus delete --force "${CONTAINER_NAME}" 2>/dev/null || true
}
trap cleanup EXIT

# ── Check prerequisites ──────────────────────────────────────────────────────

if ! command -v incus &>/dev/null; then
    echo "ERROR: 'incus' is not installed or not in PATH. Run scripts/setup-incus.sh first." >&2
    exit 1
fi

# ── Remove existing image if present ────────────────────────────────────────

if incus image list --format csv | grep -q "^${IMAGE_NAME},"; then
    log "Removing existing image '${IMAGE_NAME}'…"
    incus image delete "${IMAGE_NAME}"
fi

# ── Launch a fresh Ubuntu 24.04 container ───────────────────────────────────

log "Launching build container '${CONTAINER_NAME}' from ubuntu:24.04…"
incus launch images:ubuntu/24.04 "${CONTAINER_NAME}" --profile svkexe-default

# Wait for cloud-init / network
log "Waiting for container to be ready…"
sleep 5
incus exec "${CONTAINER_NAME}" -- bash -c "
    until systemctl is-active --quiet systemd-networkd 2>/dev/null || ip route show default &>/dev/null; do
        sleep 1
    done
"

# The bridge (svkexe-br0) is IPv4-only; force apt and curl to skip AAAA lookups.
log "Forcing IPv4 for apt and curl inside the container…"
incus exec "${CONTAINER_NAME}" -- bash -c "
    mkdir -p /etc/apt/apt.conf.d
    echo 'Acquire::ForceIPv4 \"true\";' > /etc/apt/apt.conf.d/99-force-ipv4
    mkdir -p /etc/gai.conf.d 2>/dev/null || true
    # Prefer IPv4 addresses when resolving — flips the default precedence
    # so getaddrinfo returns A records before AAAA.
    if ! grep -qs '^precedence ::ffff:0:0/96  100' /etc/gai.conf; then
        printf 'precedence ::ffff:0:0/96  100\n' >> /etc/gai.conf
    fi
    echo '--ipv4' > /root/.curlrc
"

# ── Install base packages ────────────────────────────────────────────────────

log "Installing base packages (build-essential, git, curl)…"
incus exec "${CONTAINER_NAME}" -- bash -c "
    export DEBIAN_FRONTEND=noninteractive
    apt-get update -q
    apt-get install -y --no-install-recommends \
        build-essential \
        git \
        curl \
        ca-certificates \
        gnupg \
        lsb-release \
        systemd \
        2>&1
"

# ── Install Go ───────────────────────────────────────────────────────────────

log "Installing Go ${GO_VERSION}…"
incus exec "${CONTAINER_NAME}" -- bash -c "
    ARCH=\$(dpkg --print-architecture)
    case \"\${ARCH}\" in
        amd64) GOARCH=amd64 ;;
        arm64) GOARCH=arm64 ;;
        *) echo 'Unsupported arch: '\${ARCH}; exit 1 ;;
    esac
    curl -fsSL \"https://go.dev/dl/go${GO_VERSION}.linux-\${GOARCH}.tar.gz\" -o /tmp/go.tar.gz
    rm -rf /usr/local/go
    tar -C /usr/local -xzf /tmp/go.tar.gz
    rm /tmp/go.tar.gz
    echo 'export PATH=\$PATH:/usr/local/go/bin' >> /etc/profile.d/go.sh
    chmod 644 /etc/profile.d/go.sh
"

# ── Install Node.js ──────────────────────────────────────────────────────────

log "Installing Node.js ${NODE_MAJOR}.x…"
incus exec "${CONTAINER_NAME}" -- bash -c "
    curl -fsSL https://deb.nodesource.com/setup_${NODE_MAJOR}.x | bash -
    apt-get install -y --no-install-recommends nodejs
"

# ── Create non-root user 'user' ──────────────────────────────────────────────

log "Creating user 'user'…"
incus exec "${CONTAINER_NAME}" -- bash -c "
    if ! id user &>/dev/null; then
        useradd -m -s /bin/bash user
    fi
"

# ── Create /data directory ───────────────────────────────────────────────────

log "Creating /data directory…"
incus exec "${CONTAINER_NAME}" -- bash -c "
    mkdir -p /data
    chown user:user /data
"

# ── Build and install Shelley ────────────────────────────────────────────────

log "Cloning and building Shelley from github.com/boldsoftware/shelley…"
incus exec "${CONTAINER_NAME}" -- bash -c "
    export PATH=\$PATH:/usr/local/go/bin
    export HOME=/root

    # Fetch latest release tag
    SHELLEY_TAG=\$(
        git ls-remote --tags https://github.com/boldsoftware/shelley.git \
        | grep -oP 'refs/tags/v[0-9]+\.[0-9]+\.[0-9]+\$' \
        | sort -V | tail -1 | sed 's|refs/tags/||'
    )
    echo \"Pinning Shelley to \${SHELLEY_TAG}\"

    rm -rf /tmp/shelley
    git clone --branch \"\${SHELLEY_TAG}\" --depth 1 \
        https://github.com/boldsoftware/shelley.git /tmp/shelley

    cd /tmp/shelley
    go build -ldflags '-s -w' -o /usr/local/bin/shelley .
    chmod 755 /usr/local/bin/shelley
    rm -rf /tmp/shelley

    echo \"Shelley installed: \$(/usr/local/bin/shelley --version 2>&1 || true)\"
"

# ── Create Shelley environment file ─────────────────────────────────────────

log "Creating /etc/shelley/env placeholder…"
incus exec "${CONTAINER_NAME}" -- bash -c "
    mkdir -p /etc/shelley
    # Operators should populate this file with runtime secrets/config, e.g.:
    #   ANTHROPIC_API_KEY=sk-...
    #   SHELLEY_SECRET=...
    if [ ! -f /etc/shelley/env ]; then
        cat > /etc/shelley/env <<'ENVEOF'
# Shelley runtime environment — populate before starting the service.
# Example:
# ANTHROPIC_API_KEY=sk-ant-...
ENVEOF
    fi
    chmod 640 /etc/shelley/env
    chown root:user /etc/shelley/env
"

# ── Install systemd service unit ─────────────────────────────────────────────

log "Installing shelley.service systemd unit…"
incus exec "${CONTAINER_NAME}" -- bash -c "
    cat > /etc/systemd/system/shelley.service <<'UNITEOF'
[Unit]
Description=Shelley Coding Agent
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/shelley -require-header X-ExeDev-Userid -port 9000 -db /data/shelley.db -systemd-activation
EnvironmentFile=/etc/shelley/env
WorkingDirectory=/home/user
User=user
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
UNITEOF

    systemctl enable shelley.service
"

# ── Publish the image ────────────────────────────────────────────────────────

log "Stopping container before publishing…"
incus stop "${CONTAINER_NAME}"

log "Publishing Incus image as '${IMAGE_NAME}'…"
incus publish "${CONTAINER_NAME}" --alias "${IMAGE_NAME}" \
    --compression bzip2 \
    description="svkexe base image with Shelley coding agent"

log "Done. Image '${IMAGE_NAME}' is ready."
incus image list "${IMAGE_NAME}"
