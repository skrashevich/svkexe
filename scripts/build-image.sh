#!/usr/bin/env bash
# build-image.sh — Build Incus image 'svkexe-base' from Ubuntu 24.04.
#
# A developer-ready container image with systemd, Shelley, Claude Code, Codex,
# Go, Node, Docker, Chrome, and common dev tools.
#
# Idempotent: safe to re-run. Cleans up the working container on exit.
#
# Environment overrides:
#   SKIP_CHROME=1       Skip headless Chrome install
#   SKIP_TAILSCALE=1    Skip Tailscale install
#   SKIP_CLAUDE=1       Skip Claude Code install
#   SKIP_CODEX=1        Skip Codex install
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

# ── Remove existing image if present ────────────────────────────────────────

if incus image list --format csv | grep -q "^${IMAGE_NAME},"; then
    log "Removing existing image '${IMAGE_NAME}'…"
    incus image delete "${IMAGE_NAME}"
fi

# ── Launch a fresh Ubuntu 24.04 container ───────────────────────────────────

log "Launching build container from ubuntu:24.04…"
incus launch images:ubuntu/24.04 "${CONTAINER_NAME}" --profile svkexe-default

log "Waiting for container networking…"
sleep 5
run_in "for i in {1..30}; do ip route show default &>/dev/null && break; sleep 1; done"

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
    echo 'y' | unminimize || true

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
        atop btop iotop ncdu \
        docker.io docker-buildx docker-compose-v2 \
        imagemagick ffmpeg \
        bubblewrap gh \
        nginx \
        fonts-noto-color-emoji

    # Allow non-root users to ping without sudo
    setcap cap_net_raw=+ep /usr/bin/ping || true
    fc-cache -f -v || true
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

log "Installing Node.js 20.x…"
run_in "
    curl -fsSL https://deb.nodesource.com/setup_20.x | bash -
    apt-get install -y --no-install-recommends nodejs
    corepack enable
    corepack prepare pnpm@latest --activate
"

# ── Install uv (Python package manager) ────────────────────────────────────

log "Installing uv…"
run_in "curl -LsSf https://astral.sh/uv/install.sh | env UV_INSTALL_DIR=/usr/local/bin sh"

# ── Install headless Chrome ────────────────────────────────────────────────

if [[ "${SKIP_CHROME:-0}" != "1" ]]; then
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

if [[ "${SKIP_TAILSCALE:-0}" != "1" ]]; then
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
        docker.service containerd.service \
        nginx.service tailscaled.service \
        atop.service atopacct.service \
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
    usermod -aG docker ${CONTAINER_USER} 2>/dev/null || true
    echo '${CONTAINER_USER} ALL=(ALL) NOPASSWD:ALL' >> /etc/sudoers

    # Enable linger for systemd user services.
    mkdir -p /var/lib/systemd/linger
    touch /var/lib/systemd/linger/${CONTAINER_USER}

    # Set PATH in .bashrc.
    echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> /home/${CONTAINER_USER}/.bashrc

    # Configure git defaults.
    su - ${CONTAINER_USER} -c 'git config --global init.defaultBranch main'
"

# ── Install Shelley (from release binary) ──────────────────────────────────

log "Installing Shelley…"
run_in "
    ARCH=\$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
    SHELLEY_URL=\$(curl -fsSL https://api.github.com/repos/boldsoftware/shelley/releases/latest \
        | jq -r \".assets[] | select(.name == \\\"shelley_linux_\${ARCH}\\\") | .browser_download_url\")
    if [ -z \"\${SHELLEY_URL}\" ] || [ \"\${SHELLEY_URL}\" = \"null\" ]; then
        echo 'ERROR: Could not find Shelley release binary' >&2
        exit 1
    fi
    curl -fsSL \"\${SHELLEY_URL}\" -o /usr/local/bin/shelley
    chmod +x /usr/local/bin/shelley
    echo \"Shelley installed: \$(/usr/local/bin/shelley -help 2>&1 | head -1 || echo ok)\"
"

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
    mkdir -p /data /etc/shelley
    chown ${CONTAINER_USER}:${CONTAINER_USER} /data
    cat > /etc/shelley/env <<'ENVEOF'
# Shelley runtime environment — populated by svkexe gateway.
ENVEOF
    chmod 640 /etc/shelley/env
    chown root:${CONTAINER_USER} /etc/shelley/env
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
incus publish "${CONTAINER_NAME}" --alias "${IMAGE_NAME}" \
    --compression bzip2 \
    description="svkexe base image — Ubuntu 24.04 with dev tools, Shelley, Claude Code, Codex"

log "Done. Image '${IMAGE_NAME}' is ready."
incus image list "${IMAGE_NAME}"
