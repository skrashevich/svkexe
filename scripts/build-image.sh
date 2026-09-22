#!/usr/bin/env bash
# build-image.sh — Build Incus image 'svkexe-base' from Ubuntu 24.04.
#
# A developer-ready container image with systemd, PicoClaw, Claude Code, Codex,
# Go, Node, and common dev tools.
#
# Idempotent: safe to re-run. Cleans up the working container on exit.
#
# Environment overrides:
#   INSTALL_CHROME=1    Install headless Chrome (skipped by default)
#   INSTALL_TAILSCALE=1 Install Tailscale (skipped by default)
#   INSTALL_DOCKER=1    Install Docker-in-Docker (skipped by default)
#   SKIP_CLAUDE=1       Skip Claude Code install
#   SKIP_CODEX=1        Skip Codex install
#   SVKEXE_AGENT_BINARY=/path/to/picoclaw
#                       Use a prebuilt Linux agent instead of building one, for
#                       hosts without a Go/Node toolchain
set -euo pipefail

if [[ ! -t 0 ]]; then
    exec </dev/null
fi

IMAGE_NAME="svkexe-base"
CONTAINER_NAME="svkexe-build-$$"
BRIDGE_GATEWAY="10.100.0.1"
CONTAINER_USER="user"

# ── Helpers ─────────────────────────────────────────────────────────────────

log() { echo "[build-image] $*"; }
die() { echo "[build-image ERROR] $*" >&2; exit 1; }

run_in() { incus exec "${CONTAINER_NAME}" -- bash -c "$1"; }

cleanup() {
    log "Cleaning up working container…"
    incus delete --force "${CONTAINER_NAME}" 2>/dev/null || true
}
trap cleanup EXIT

# ── Check prerequisites ──────────────────────────────────────────────────────

command -v incus &>/dev/null || die "'incus' is not installed. Run scripts/setup-incus.sh first."

# Keep the previous image available until the replacement is published.

# ── Launch a fresh Ubuntu 24.04 container ───────────────────────────────────

log "Launching build container from ubuntu:24.04…"
incus launch images:ubuntu/24.04 "${CONTAINER_NAME}" --profile svkexe-default

log "Waiting for container networking…"
sleep 5
run_in "for i in {1..30}; do ip route show default | grep -q . && exit 0; sleep 1; done; exit 1"

# ── DNS ─────────────────────────────────────────────────────────────────────

log "Configuring DNS…"
run_in "
    systemctl disable --now systemd-resolved 2>/dev/null || true
    rm -f /etc/resolv.conf
    cat > /etc/resolv.conf <<'EOF'
nameserver ${BRIDGE_GATEWAY}
nameserver 1.1.1.1
nameserver 8.8.8.8
EOF
    chmod 644 /etc/resolv.conf
"

# ── Force IPv4 ──────────────────────────────────────────────────────────────

log "Forcing IPv4 for apt and curl…"
run_in "
    echo 'Acquire::ForceIPv4 \"true\";' > /etc/apt/apt.conf.d/99-force-ipv4
    grep -qs '^precedence ::ffff:0:0/96  100' /etc/gai.conf || \
        printf 'precedence ::ffff:0:0/96  100\n' >> /etc/gai.conf
    echo '--ipv4' > /root/.curlrc
"

# ── Unminimize and install packages ─────────────────────────────────────────

log "Unminimizing Ubuntu and installing packages (this takes a while)…"
run_in "
    export DEBIAN_FRONTEND=noninteractive
    rm -f /etc/dpkg/dpkg.cfg.d/excludes /etc/dpkg/dpkg.cfg.d/01_nodoc
    echo 'debconf debconf/frontend select Noninteractive' | debconf-set-selections
    if command -v unminimize &>/dev/null; then
        echo 'y' | unminimize
    fi

    apt-get update -q
    apt-get install -y --no-install-recommends \
        build-essential ca-certificates curl wget git jq \
        sqlite3 vim neovim ripgrep tree file unzip rsync \
        lsof iproute2 less net-tools socat netcat-openbsd \
        openssh-server openssh-client \
        python3-pip python-is-python3 pipx \
        make pkg-config libcap2-bin \
        psmisc bsdmainutils sudo \
        iputils-ping util-linux \
        gnupg lsb-release \
        man-db manpages manpages-dev \
        systemd systemd-sysv dbus-user-session \
        btop ncdu \
        bubblewrap gh

    # Allow non-root users to ping without sudo
    setcap cap_net_raw=+ep /usr/bin/ping || true
    rm -f /usr/sbin/policy-rc.d
"

# ── Install Go (latest stable) ─────────────────────────────────────────────

log "Installing Go (latest stable)…"
run_in "
    ARCH=\$(dpkg --print-architecture)
    GO_VERSION=\$(curl -fsSL 'https://go.dev/dl/?mode=json' | jq -r '.[0].version')
    curl -fsSL \"https://go.dev/dl/\${GO_VERSION}.linux-\${ARCH}.tar.gz\" | tar -xzC /usr/local
    ln -sf /usr/local/go/bin/go /usr/local/bin/go
    ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
    echo 'export PATH=\$PATH:/usr/local/go/bin' > /etc/profile.d/go.sh
    chmod 644 /etc/profile.d/go.sh
"

# ── Install Node.js ─────────────────────────────────────────────────────────

log "Installing Node.js 22.x…"
run_in "
    curl -fsSL https://deb.nodesource.com/setup_22.x | bash -
    apt-get install -y --no-install-recommends nodejs
    corepack enable
    corepack prepare pnpm@latest --activate
"

# ── Install uv (Python package manager) ────────────────────────────────────

log "Installing uv…"
run_in "curl -LsSf https://astral.sh/uv/install.sh | env UV_INSTALL_DIR=/usr/local/bin sh"

# ── Install headless Chrome ────────────────────────────────────────────────

if [[ "${INSTALL_CHROME:-0}" == "1" ]]; then
    log "Installing headless Chrome…"
    run_in "
        apt-get install -y --no-install-recommends \
            libglib2.0-0 libnss3 libx11-6 libxcomposite1 libxdamage1 \
            libxext6 libxi6 libxrandr2 libgbm1 libgtk-3-0 \
            chromium-browser || \
        apt-get install -y --no-install-recommends chromium || true
    "
fi

# ── Install Tailscale ──────────────────────────────────────────────────────

if [[ "${INSTALL_TAILSCALE:-0}" == "1" ]]; then
    log "Installing Tailscale…"
    run_in "
        curl -fsSL https://pkgs.tailscale.com/stable/ubuntu/noble.noarmor.gpg \
            -o /usr/share/keyrings/tailscale-archive-keyring.gpg
        curl -fsSL https://pkgs.tailscale.com/stable/ubuntu/noble.tailscale-keyring.list \
            -o /etc/apt/sources.list.d/tailscale.list
        apt-get update -q
        apt-get install -y --no-install-recommends tailscale
    "
fi

# ── Install Docker (opt-in) ────────────────────────────────────────────────

if [[ "${INSTALL_DOCKER:-0}" == "1" ]]; then
    log "Installing Docker…"
    run_in "
        export DEBIAN_FRONTEND=noninteractive
        apt-get install -y --no-install-recommends \
            docker.io docker-buildx docker-compose-v2
    "
fi

# ── Configure systemd ──────────────────────────────────────────────────────

log "Tuning systemd for container use…"
run_in "
    # Mask services that are useless or broken inside containers.
    systemctl mask -- \
        getty.target console-getty.service \
        fwupd.service fwupd-refresh.service fwupd-refresh.timer \
        systemd-random-seed.service \
        systemd-resolved.service \
        systemd-remount-fs.service \
        systemd-sysusers.service \
        systemd-update-done.service \
        systemd-update-utmp.service \
        systemd-journal-catalog-update.service \
        systemd-modules-load.service \
        systemd-udevd.service systemd-udevd-control.service \
        systemd-udevd-kernel.service systemd-udev-trigger.service \
        systemd-udev-settle.service systemd-hwdb-update.service \
        modprobe@.service \
        iscsid.socket dm-event.socket lvm2-lvmpolld.socket \
        man-db.timer dpkg-db-backup.timer e2scrub_all.timer \
        apt-daily-upgrade.timer apt-daily.timer \
        ssh.socket ssh.service \
        2>/dev/null || true

    # Disable services that can be enabled later if needed.
    systemctl disable -- \
        ufw.service \
        snapd.socket snapd.snap-repair.timer snapd.snap-repair.service \
        motd-news.timer motd-news.service \
        unattended-upgrades.service \
        2>/dev/null || true

    # Tuned systemd config for containers.
    mkdir -p /etc/systemd/system.conf.d
    cat > /etc/systemd/system.conf.d/container.conf <<'SYSD'
[Manager]
LogLevel=info
LogTarget=console
DefaultOOMPolicy=continue
SYSD

    mkdir -p /etc/systemd/journald.conf.d
    cat > /etc/systemd/journald.conf.d/persistent.conf <<'JRNL'
[Journal]
Storage=persistent
JRNL

    systemctl set-default multi-user.target
"

# ── Create user ─────────────────────────────────────────────────────────────

log "Creating user '${CONTAINER_USER}'…"
run_in "
    if ! id ${CONTAINER_USER} &>/dev/null; then
        useradd -m -s /bin/bash ${CONTAINER_USER}
    fi
    usermod -aG sudo ${CONTAINER_USER}
    echo '${CONTAINER_USER} ALL=(ALL) NOPASSWD:ALL' >> /etc/sudoers

    # Enable linger for systemd user services.
    mkdir -p /var/lib/systemd/linger
    touch /var/lib/systemd/linger/${CONTAINER_USER}

    # Set PATH in .bashrc.
    echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> /home/${CONTAINER_USER}/.bashrc

    # Configure git defaults.
    su - ${CONTAINER_USER} -c 'git config --global init.defaultBranch main'
"

# ── Install the pinned PicoClaw agent with Shelley UI/prompts ──────────────

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Accept a prebuilt agent so the image can be rebuilt on a host that has no Go
# or Node toolchain. Same variable the gateway uses to pick an explicit artifact.
AGENT_BINARY="${SVKEXE_AGENT_BINARY:-}"
if [[ -n "$AGENT_BINARY" ]]; then
    [[ -f "$AGENT_BINARY" ]] || die "SVKEXE_AGENT_BINARY=$AGENT_BINARY does not exist"
    log "Using prebuilt PicoClaw agent: $AGENT_BINARY"
else
    log "Building PicoClaw agent…"
    AGENT_GOOS=linux "$REPO_ROOT/scripts/build-agent.sh"
    AGENT_BINARY="$REPO_ROOT/bin/picoclaw"
fi
incus file push "$AGENT_BINARY" "${CONTAINER_NAME}/usr/local/bin/picoclaw"
run_in "chmod 755 /usr/local/bin/picoclaw; mkdir -p /usr/local/share/licenses/svkexe-agent"
# Refuse to bake in a wrong or foreign artifact: it must run on the guest and
# report itself as the svkexe integration build.
run_in "/usr/local/bin/picoclaw version" | grep -q '"customized": *true' \
    || die "agent artifact is not the svkexe PicoClaw integration build"
for license in "$REPO_ROOT"/agent/licenses/*; do
    incus file push "$license" "${CONTAINER_NAME}/usr/local/share/licenses/svkexe-agent/$(basename "$license")"
done
# Gateway setup writes the service/config after provisioning each VM. Avoid
# starting an unconfigured agent in the base image.

# ── Install Claude Code ────────────────────────────────────────────────────

if [[ "${SKIP_CLAUDE:-0}" != "1" ]]; then
    log "Installing Claude Code…"
    run_in "
        mkdir -p /home/${CONTAINER_USER}/.local/bin
        ARCH=\$(uname -m | sed 's/x86_64/x64/;s/aarch64/arm64/')
        PLATFORM=\"linux-\${ARCH}\"
        STABLE=\$(curl -fsSL 'https://storage.googleapis.com/claude-code-dist-86c565f3-f756-42ad-8dfa-d59b1c096819/claude-code-releases/stable')
        HASH=\$(curl -fsSL \"https://storage.googleapis.com/claude-code-dist-86c565f3-f756-42ad-8dfa-d59b1c096819/claude-code-releases/\${STABLE}/manifest.json\" \
            | jq -r \".platforms[\\\"\${PLATFORM}\\\"].checksum\")
        curl -fsSL \"https://storage.googleapis.com/claude-code-dist-86c565f3-f756-42ad-8dfa-d59b1c096819/claude-code-releases/\${STABLE}/\${PLATFORM}/claude\" \
            -o /home/${CONTAINER_USER}/.local/bin/claude
        echo \"\${HASH}  /home/${CONTAINER_USER}/.local/bin/claude\" | sha256sum -c -
        chmod +x /home/${CONTAINER_USER}/.local/bin/claude
        chown -R ${CONTAINER_USER}:${CONTAINER_USER} /home/${CONTAINER_USER}/.local
        ln -sf /home/${CONTAINER_USER}/.local/bin/claude /usr/local/bin/claude
    "
fi

# ── Install Codex ──────────────────────────────────────────────────────────

if [[ "${SKIP_CODEX:-0}" != "1" ]]; then
    log "Installing Codex…"
    run_in "
        ARCH=\$(uname -m)
        case \${ARCH} in
            x86_64)  CODEX_ARCH='x86_64-unknown-linux-musl' ;;
            aarch64) CODEX_ARCH='aarch64-unknown-linux-musl' ;;
            *) echo 'Unsupported arch' >&2; exit 1 ;;
        esac
        CODEX_VERSION=\$(curl -fsSL https://api.github.com/repos/openai/codex/releases/latest | jq -r '.tag_name')
        curl -fsSL \"https://github.com/openai/codex/releases/download/\${CODEX_VERSION}/codex-\${CODEX_ARCH}.tar.gz\" \
            | tar -xzC /usr/local/bin
        mv \"/usr/local/bin/codex-\${CODEX_ARCH}\" /usr/local/bin/codex 2>/dev/null || true
        chmod +x /usr/local/bin/codex
    "
fi

# ── Create svkexe directories ──────────────────────────────────────────────

log "Creating svkexe directories…"
run_in "
    mkdir -p /data /etc/picoclaw
    chown ${CONTAINER_USER}:${CONTAINER_USER} /data
    cat > /etc/picoclaw/env <<'ENVEOF'
# Agent runtime environment — populated by svkexe gateway.
ENVEOF
    chmod 640 /etc/picoclaw/env
    chown root:${CONTAINER_USER} /etc/picoclaw/env

    echo '{}' > /etc/picoclaw/picoclaw.json
    chmod 640 /etc/picoclaw/picoclaw.json
    chown root:${CONTAINER_USER} /etc/picoclaw/picoclaw.json
"

# ── MOTD ────────────────────────────────────────────────────────────────────

log "Configuring MOTD…"
run_in "
    rm -rf /etc/update-motd.d/* /etc/motd
    touch /home/${CONTAINER_USER}/.hushlogin
    chown ${CONTAINER_USER}:${CONTAINER_USER} /home/${CONTAINER_USER}/.hushlogin

    cat >> /home/${CONTAINER_USER}/.bashrc <<'MOTD'

# svkexe VM
if [[ \$- == *i* ]]; then
    echo \"\"
    echo \"You are on \$(hostname). The disk is persistent. You have 'sudo'.\"
    echo \"\"
fi
MOTD
    chown ${CONTAINER_USER}:${CONTAINER_USER} /home/${CONTAINER_USER}/.bashrc
"

# ── Cleanup apt caches ─────────────────────────────────────────────────────

log "Cleaning up…"
run_in "
    apt-get clean
    rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/*
"

# ── Publish the image ────────────────────────────────────────────────────────

log "Stopping container before publishing…"
incus stop "${CONTAINER_NAME}"

log "Publishing Incus image as '${IMAGE_NAME}'…"
incus publish "${CONTAINER_NAME}" --reuse --alias "${IMAGE_NAME}" \
    --compression bzip2 \
    description="svkexe base image — Ubuntu 24.04 with PicoClaw, dev tools, Claude Code, Codex"

log "Done. Image '${IMAGE_NAME}' is ready."
incus image list "${IMAGE_NAME}"
