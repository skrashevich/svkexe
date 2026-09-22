---
title: Deployment guide
description: Install, configure and update a svkexe host
---

# Deployment guide

Use your own domain throughout. `example.com` is a placeholder, not a service
provided by this project. The gateway derives VM and agent URLs from `DOMAIN`;
base-domain and wildcard DNS records must point to your host.

## Prerequisites

- A Linux host with systemd: Ubuntu 24.04/26.04 LTS or Debian 12/13, amd64 or arm64.
- Root access, outbound HTTPS for APT, Go/npm modules and image downloads, and
  enough RAM/disk for the agent UI build and your VMs.
- Base and wildcard DNS (`example.com`, `*.example.com`). Use DNS-only records
  for direct access; a CDN needs separate consideration for SSH and custom-domain
  DNS verification (`GATEWAY_PUBLIC_IPS`).
- Inbound TCP 80/443 for Caddy and 2222 for the SSH gateway. Keep the host's own
  administrative SSH port accessible. The Incus subnet `10.100.0.0/24` must not
  overlap another network on the host.

The installer uses [Zabbly's Incus packages](https://github.com/zabbly/incus).
For manual builds, install Go sufficient for **both** `go.mod` and
`agent/shelley/go.mod` (currently 1.26.2 and 1.27.1), make, Python 3 and Node/npm.
The agent build bootstraps its pinned Node/pnpm versions; see [PICOCLAW.md](PICOCLAW.md).

## Bare metal (recommended)

```bash
curl -fsSL https://raw.githubusercontent.com/skrashevich/svkexe/main/scripts/install.sh \
  | sudo env DOMAIN=example.com bash
```

From a checkout, use `sudo env DOMAIN=example.com ./scripts/install.sh`.
The installer installs packages/toolchains, configures Incus, builds the base
image and both binaries, creates `/etc/svkexe/gateway.env`, and enables the
systemd gateway and update units. **The gateway is not started automatically.**
TLS, backup scheduling and monitoring require the steps below.

Review the generated config with `sudoedit /etc/svkexe/gateway.env`:

- `DOMAIN`: your real base domain. Login through this hostname, since the
  session cookie is scoped to it; an IP-address login does not work with a
  different configured cookie domain.
- `BOOTSTRAP_ADMIN_EMAIL` and `BOOTSTRAP_ADMIN_PASSWORD`: the initial account.
  The password is reapplied on startup; change this value to rotate it.
- `GATEWAY_ENC_KEY`: generated AES key. Preserve it across reinstalls and backups.
- `GATEWAY_COOKIE_SECURE=1` for HTTPS. Keep `0` only during HTTP testing.
- `OPENROUTER_API_KEY`: optional platform fallback; users can instead add their
  own provider connections after login.
- `METADATA_ADDR`: defaults to `10.100.0.1:8081`. If changing the port, run
  `sudo env SVKEXE_METADATA_PORT=NEW_PORT ./scripts/install-metadata-units.sh`
  and use that same override on future installation/update runs.

```bash
sudo systemctl start svkexe-gateway
sudo systemctl status svkexe-gateway --no-pager
sudo journalctl -u svkexe-gateway -n 50 --no-pager
curl -f -H 'Host: example.com' http://127.0.0.1:8080/login
```

For temporary HTTP access use `http://example.com:8080/login`. Production uses
`https://example.com/login` after configuring TLS below.

Re-running preserves an existing `gateway.env` and base image, but refreshes
packages, profile settings, binaries and units. It can affect live networking;
use a dedicated checkout and review changes before running on an existing host.
Skip flags: `SKIP_DOCKER`, `SKIP_INCUS`, `SKIP_IMAGE_BUILD`, `SKIP_GO`,
`SKIP_BUILD`, `SKIP_SERVICE` (set to `1`). `SKIP_INCUS` also skips host setup,
metadata and image creation. `SKIP_BUILD` still allows the image step to build
an agent. Source overrides: `SVKEXE_REPO`, `SVKEXE_BRANCH` (branch or tag),
`SVKEXE_SRC_DIR` (default `/opt/svkexe` for piped installation).

## TLS for the bare-metal gateway

Authentication is the gateway's own login and session cookie. Authelia is not
required. The supplied Caddyfile uses the Cloudflare DNS plugin for wildcard
certificates and gateway-approved on-demand certificates for custom domains.
For another DNS provider, change the Caddy module and `tls dns` configuration.
[Caddy's build documentation](https://caddyserver.com/docs/build) describes plugins.

One way to run this Caddyfile with the bare-metal gateway is a host-networked
Caddy container. Do not start the full Compose gateway alongside the systemd one.
From the source checkout:

```bash
sudo install -d -m 0750 /etc/svkexe
sudo install -m 0644 deploy/Caddyfile /etc/svkexe/Caddyfile
sudoedit /etc/svkexe/caddy.env
```

Save these values (replace the examples), then protect the file:

```dotenv
DOMAIN=example.com
ACME_EMAIL=admin@example.com
CLOUDFLARE_API_TOKEN=your-zone-dns-token
GATEWAY_UPSTREAM=127.0.0.1:8080
```

```bash
sudo chmod 600 /etc/svkexe/caddy.env
sudo docker build -f deploy/Caddy.Dockerfile -t svkexe-caddy deploy
sudo docker run -d --name svkexe-caddy --restart unless-stopped \
  --network host --env-file /etc/svkexe/caddy.env \
  -v /etc/svkexe/Caddyfile:/etc/caddy/Caddyfile:ro \
  -v svkexe-caddy-data:/data -v svkexe-caddy-config:/config svkexe-caddy
```

Set `GATEWAY_COOKIE_SECURE=1` in `gateway.env` and restart `svkexe-gateway`.
Restrict external access to port 8080 with the host firewall; keep the gateway
reachable from Caddy and, if using a direct internal LLM URL, from the VM bridge.
Caddy strips incoming identity headers. Do not inject identity as an alternative
to logging in: the gateway authenticates the session itself.

## Docker Compose (alternative gateway deployment)

Incus still runs on the **Linux host**. A create-VM request goes from the Docker
gateway through its mounted Incus socket to the host daemon. Incus creates an
LXC system container from `svkexe-base`, with `svkexe-default`, storage in
`svkexe-pool` and networking on `svkexe-br0`. VM disks belong to Incus on the host;
only the gateway database and keys belong to the `gateway_data` Docker volume.
The gateway must also be able to reach VM IPs on `10.100.0.0/24` for HTTP/SSE.

Prepare the host using the installer but omit
the systemd gateway and host binary installation:

```bash
git clone https://github.com/skrashevich/svkexe
cd svkexe
sudo env DOMAIN=example.com SKIP_BUILD=1 SKIP_SERVICE=1 ./scripts/install.sh
cd deploy
cp .env.example .env
chmod 600 .env
```

Edit `.env`: use your own domain, ACME email, Cloudflare zone DNS token, admin
email/password, and independently generated `GATEWAY_ENC_KEY` and
`LLM_INTERNAL_TOKEN` (`openssl rand -hex 32`). Keep these secrets for updates.
Compose refuses to start with empty required values.

```bash
sudo docker compose config --quiet
sudo docker compose up -d --build
sudo docker compose ps
sudo docker compose logs --tail=50 gateway caddy
```

Login at `https://example.com/login` using the configured bootstrap account.
The gateway mounts the Incus socket, persists SQLite/SSH keys in `gateway_data`,
and exposes SSH on 2222 (`SSH_PORT` changes the host mapping). HTTP is internal
to Compose. Do not use `docker compose down -v` unless deliberately deleting data.

Instance metadata is disabled (`METADATA_ADDR=off`) because this gateway has a
separate network namespace. Systemd self-update is unavailable in Compose;
update the source and run `docker compose up -d --build`. New base-image inputs
also require `sudo ./scripts/build-image.sh` from the repository root.
If `SSH_PORT` changes, use that external port in client commands; gateway-generated
examples still reflect its internal `SSH_ADDR` port.

## Verify the complete installation

1. Check the gateway log for Incus, metadata and agent setup errors. Confirm
   `sudo incus image info svkexe-base` and `sudo incus profile show svkexe-default`.
2. Open `https://example.com/login`, log in, add an SSH key and an LLM connection.
3. Create a VM. Open its terminal and PicoClaw interface; send a prompt and check
   streamed output and a tool call with your configured model.
4. Connect with `ssh -p 2222 svkexe@example.com 'help --json'`, then directly to
   the created VM using the command shown in the dashboard.
5. Run a service on the VM's configured port (default 3000), open its workload
   URL, and check Private/Public access from a signed-out browser.
6. On bare metal, from the VM run
   `curl -f http://169.254.169.254/latest/meta-data/instance-id`.
   Confirm `svkexe-metadata.service` and the configured listener if this fails.

A successful build or `docker compose config` does not prove VM networking,
TLS issuance or a live model works. These checks require the target host and DNS.

## Updates and backups

Bare metal: `sudo /opt/svkexe/scripts/update.sh`, or **System → Update now**.
Use the actual checkout path if installed elsewhere. The updater resets its
checkout to the selected upstream branch/tag; keep local edits elsewhere.
`SKIP_RESTART=1` installs binaries and may rebuild the image, but leaves the
current gateway process running. It is not a build-only mode.

The update installs the new gateway and agent; existing running agents migrate
at gateway startup, stopped VMs on their next start. Image rebuilds keep the
previous image until publication of the replacement. Unaliased old images may
remain; inspect them before manually removing any.

Backups are **not scheduled by the installer**. Install a cron entry if wanted:

```cron
0 3 * * * root /opt/svkexe/scripts/backup.sh >> /var/log/svkexe-backup.log 2>&1
```

This uses SQLite's online backup and snapshots running Incus containers, with
7-day retention by default (`BACKUP_DIR`, `RETENTION_DAYS`, `GATEWAY_DB_PATH`).
Snapshots remain on the same host and are not disaster recovery. Back up the
configuration/encryption key separately and export/copy data off-host. The script
currently selects all running Incus containers, including ones outside svkexe.

Use `scripts/restore.sh --db-backup PATH` and/or
`--container INCUS_NAME --snapshot SNAPSHOT_NAME`. It asks for confirmation and
stops/restarts an active systemd gateway for DB restore. For Compose, stop the
gateway yourself and restore the database in its volume; the script does not
manage Docker services. No RPO/RTO guarantee is implied by these scripts.

## Monitoring

`GET /metrics` is unauthenticated. Actual metric names include
`svkexe_http_requests_total`, `svkexe_http_request_duration_seconds`,
`svkexe_containers_total`, `svkexe_proxy_requests_total`, and
`svkexe_ssh_sessions_active`, alongside Go/process metrics.

Compose monitoring is optional: set `GRAFANA_ADMIN_PASSWORD` and run
`docker compose --profile monitoring up -d`. Grafana is bound to
`127.0.0.1:3001`; use an SSH tunnel for remote access. Add the Prometheus data
source `http://prometheus:9090` and create/import dashboards yourself; no Grafana
dashboards are provisioned by this repository.

See [README configuration](../README.md#configuration), [API](API.md),
[SSH](SSH.md), [named VM access](ACCESS.md), and [agent migration](PICOCLAW.md).
`PLAN.md` and dated deployment reports are historical records, not install guides.
