#!/usr/bin/env bash
# install-metadata-units.sh — Install the host-side plumbing behind the instance
# metadata service the gateway serves at http://169.254.169.254/latest/meta-data/.
#
# Usage (on the target server):
#
#   sudo /opt/svkexe/scripts/install-metadata-units.sh
#
# Environment overrides:
#   SVKEXE_BRIDGE        Incus bridge VMs are attached to (default: svkexe-br0)
#   SVKEXE_METADATA_PORT Port the gateway serves metadata on (default: 8081).
#                        Must agree with the port in the gateway's METADATA_ADDR,
#                        which defaults to the bridge address on 8081. Changing
#                        one without the other leaves the redirect pointing at
#                        nothing, and a VM's request is refused by the host.
#   SVKEXE_PROFILE       Incus profile whose NIC gets anti-spoof filtering
#                        (default: svkexe-default)
#   SVKEXE_SYSTEMD_DIR   Unit directory (default: /etc/systemd/system)
#
# Two things have to be true for the service to be both reachable and sound.
#
# 1. A VM's packet to 169.254.169.254 must arrive at the gateway. The VM's
#    ordinary default route already carries it to the host — the kernel does not
#    special-case link-local IPv4 on output — so all the host has to do is
#    redirect it, on the bridge only, to the port the gateway listens on.
#
#    The host deliberately does NOT take 169.254.169.254 as an address of its
#    own. Doing that would install a route in the local table, and the host's own
#    traffic to that address would stop leaving the machine — which on any cloud
#    VPS (AWS, GCP, Azure, Hetzner, Oracle, DigitalOcean all use this exact
#    address) means the host loses its provider metadata: IAM credential refresh,
#    guest agent, OS Login key propagation. A redirect scoped to the bridge keeps
#    the host's own access intact, needs no privileged port from the gateway, and
#    cannot be reached from the host's LAN.
#
#    It also closes a hole that predates the metadata service: on such a VPS a
#    tenant VM's request to 169.254.169.254 was forwarded by the host to the
#    provider's metadata service. Now it is answered by svkexe instead.
#
# 2. A VM must not be able to send as another VM's address. Identity here is the
#    source address and nothing else, and a tenant is root inside their own VM —
#    without Incus's ipv4_filtering they can add a neighbour's address, answer
#    ARP for it, and be served the neighbour's identity, SSH keys and task. The
#    profile's NIC is therefore pinned here. The gateway refuses to answer any
#    instance whose NIC is unfiltered, so this is a prerequisite, not a hardening
#    nicety.
#
# Incus applies the NIC change to running instances too, not only at their next
# start, so the filtering normally takes effect as soon as the profile is edited.
# Do not treat "it only applies on restart" as a safety margin: it is not one.
#
# The reverse is worth knowing before running this on a busy host: if Incus pins
# an address that differs from the lease a running VM is actually using, that VM
# loses networking until it renews or restarts. Incus reuses the existing lease
# where it can find one, so this is unlikely rather than impossible.
#
# This is the single source of truth for both. scripts/install.sh and
# scripts/update.sh call it, so the plumbing travels with the code.
#
# Idempotent and safe to re-run.

set -euo pipefail

log()  { printf '\033[1;34m[metadata-units]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[metadata-units]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[metadata-units ERROR]\033[0m %s\n' "$*" >&2; exit 1; }

[[ "${EUID}" -eq 0 ]] || die "Must run as root. Try: sudo $0"

SVKEXE_BRIDGE="${SVKEXE_BRIDGE:-svkexe-br0}"
SVKEXE_METADATA_PORT="${SVKEXE_METADATA_PORT:-8081}"
SVKEXE_PROFILE="${SVKEXE_PROFILE:-svkexe-default}"
SVKEXE_SYSTEMD_DIR="${SVKEXE_SYSTEMD_DIR:-/etc/systemd/system}"

METADATA_UNIT="svkexe-metadata.service"
METADATA_FILE="${SVKEXE_SYSTEMD_DIR}/${METADATA_UNIT}"

# The link-local address the EC2 metadata API lives at. It is the same constant
# as metadata.Address in the Go code; changing one without the other breaks the
# feature silently, so it is spelled out here rather than derived.
METADATA_ADDRESS="169.254.169.254"

# ── Pre-flight ───────────────────────────────────────────────────────────────
#
# Checked here rather than left to the unit, because a failure inside the unit's
# ExecStart is reported as "waiting for the bridge" whatever actually went wrong,
# and an operator would go looking at a bridge that is already there.

command -v iptables &>/dev/null \
    || die "iptables is required for the metadata redirect but was not found."
iptables -t nat -L -n >/dev/null 2>&1 \
    || die "The iptables 'nat' table is unavailable (no nf_nat?), so the metadata redirect cannot be installed."

# ── The redirect, on the bridge only ─────────────────────────────────────────

log "Installing ${METADATA_UNIT}…"
cat >"${METADATA_FILE}" <<EOF
[Unit]
Description=svkexe instance metadata redirect on the VM bridge
Documentation=https://github.com/skrashevich/svkexe
After=incus.service
Wants=incus.service
# Deliberately not ordered before the gateway. The gateway binds its port with
# IP_FREEBIND, so it does not need the bridge to exist first, and ordering it
# after a unit that waits for the bridge would put that wait in front of the
# dashboard on every slow boot.

[Service]
Type=oneshot
RemainAfterExit=yes
# The bridge is Incus's and may not exist yet on a first boot, so wait briefly
# for it. '-C … || -I …' is what makes re-running this a no-op: the rule is
# inserted only when an identical one is not already there.
ExecStart=/bin/bash -c 'for _ in \$(seq 1 30); do ip link show ${SVKEXE_BRIDGE} &>/dev/null && break; sleep 1; done; iptables -t nat -C PREROUTING -i ${SVKEXE_BRIDGE} -d ${METADATA_ADDRESS} -p tcp --dport 80 -j REDIRECT --to-ports ${SVKEXE_METADATA_PORT} 2>/dev/null || iptables -t nat -I PREROUTING -i ${SVKEXE_BRIDGE} -d ${METADATA_ADDRESS} -p tcp --dport 80 -j REDIRECT --to-ports ${SVKEXE_METADATA_PORT}'
# The redirect covers tcp/80 to one address. The rest of 169.254.0.0/16 is a
# provider's business, not a tenant's: on a cloud host a VM could otherwise reach
# 169.254.169.253 (DNS), 169.254.169.123 (NTP) or 169.254.170.2 (task
# credentials) through the host's forwarding. RFC 3927 says link-local must not
# be forwarded anyway, so dropping it is correcting an oversight, not a policy.
#
# Docker loads br_netfilter, which puts bridged VM-to-VM traffic through the
# FORWARD chain too, so this reaches further than '-i ${SVKEXE_BRIDGE}' suggests:
# one VM cannot reach another on a 169.254 address either. Nothing legitimate
# does — IPv4LL only appears after DHCP has already failed, and mDNS is multicast.
ExecStart=/bin/bash -c 'iptables -C FORWARD -i ${SVKEXE_BRIDGE} -d 169.254.0.0/16 -j DROP 2>/dev/null || iptables -I FORWARD -i ${SVKEXE_BRIDGE} -d 169.254.0.0/16 -j DROP'
ExecStop=/bin/bash -c 'iptables -t nat -D PREROUTING -i ${SVKEXE_BRIDGE} -d ${METADATA_ADDRESS} -p tcp --dport 80 -j REDIRECT --to-ports ${SVKEXE_METADATA_PORT} 2>/dev/null || true'
ExecStop=/bin/bash -c 'iptables -D FORWARD -i ${SVKEXE_BRIDGE} -d 169.254.0.0/16 -j DROP 2>/dev/null || true'

[Install]
WantedBy=multi-user.target
EOF
chmod 0644 "${METADATA_FILE}"

systemctl daemon-reload
systemctl enable "${METADATA_UNIT}" >/dev/null

# Enabling makes the redirect survive a reboot; restarting makes it work now. A
# host with no bridge yet — a first install that has not run setup-incus.sh — is
# not a failure worth aborting over, since the unit waits for the bridge on every
# boot anyway.
if ! systemctl restart "${METADATA_UNIT}"; then
    if ip link show "${SVKEXE_BRIDGE}" &>/dev/null; then
        # The bridge is there, so waiting will not help and the operator needs
        # to see the unit's own error rather than a reassuring guess.
        warn "${METADATA_UNIT} failed even though ${SVKEXE_BRIDGE} exists. Metadata will not answer:"
        systemctl status --no-pager --lines=20 "${METADATA_UNIT}" >&2 || true
    else
        warn "${METADATA_UNIT} did not come up — metadata will start answering once ${SVKEXE_BRIDGE} exists."
    fi
fi

# ── Anti-spoof filtering on the VM NIC ───────────────────────────────────────
#
# Set on the profile rather than on each instance because the NIC itself comes
# from the profile: an instance that inherits it has no device of its own to
# amend, and adding one per VM would mean every VM carrying a copy of the NIC.

if ! command -v incus &>/dev/null; then
    warn "incus not found — could not pin ${SVKEXE_PROFILE}'s NIC. The gateway will refuse to answer metadata for unfiltered VMs."
elif ! incus profile show "${SVKEXE_PROFILE}" &>/dev/null; then
    warn "Incus profile ${SVKEXE_PROFILE} does not exist yet — run scripts/setup-incus.sh, which sets the same keys."
else
    log "Pinning ${SVKEXE_PROFILE}'s NIC to its allocated address and MAC…"
    # Not a warning. The gateway treats this as a hard prerequisite and refuses
    # to serve metadata to every VM without it, so a yellow line in an unattended
    # update log is not enough for the operator to act on.
    incus profile device set "${SVKEXE_PROFILE}" eth0 \
        security.ipv4_filtering=true \
        security.mac_filtering=true \
        || die "Could not pin ${SVKEXE_PROFILE}'s NIC. Until this succeeds the gateway refuses to serve metadata to any VM, because an unpinned source address cannot be trusted."
fi

log "Done. The gateway serves instance metadata at http://${METADATA_ADDRESS}/latest/meta-data/ for its VMs."
log "The gateway must be listening on port ${SVKEXE_METADATA_PORT} of ${SVKEXE_BRIDGE}'s own address for the redirect to land — see METADATA_ADDR."
