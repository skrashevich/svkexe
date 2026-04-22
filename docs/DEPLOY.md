# Deployment Guide

## Prerequisites

- Linux host (Ubuntu 24.04 LTS or Debian 12 recommended)
- [Incus](https://linuxcontainers.org/incus/docs/main/) v6.0+ installed and running
- Go 1.22+ (for building from source)
- [Caddy](https://caddyserver.com/) v2.8+ for TLS termination and reverse proxying
- [Authelia](https://www.authelia.com/) v4.38+ for authentication
- A domain name with DNS wildcard support (`*.yourdomain.com`)
- SQLite (included via Go driver — no separate install needed)

## Quick Start

```bash
# 1. Clone the repository
git clone https://github.com/skrashevich/svkexe
cd platform

# 2. Build the gateway binary
go build -o bin/gateway ./cmd/gateway

# 3. Create the data directory
sudo mkdir -p /var/lib/svkexe
sudo chown $(whoami) /var/lib/svkexe

# 4. Set required environment variables (see below)
export GATEWAY_ENC_KEY="$(openssl rand -hex 32)"
export DOMAIN="yourdomain.com"

# 5. Run the gateway
./bin/gateway
```

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `GATEWAY_ADDR` | `:8080` | HTTP listen address for the API gateway |
| `GATEWAY_DB_PATH` | `/var/lib/svkexe/gateway.db` | Path to the SQLite database file |
| `GATEWAY_ENC_KEY` | *(empty — dev mode only)* | 32-byte hex key for AES-256 encryption of secrets at rest. **Required in production.** |
| `DOMAIN` | *(empty)* | Base domain for subdomain-based container routing (e.g. `example.com`). Required for container proxy. |
| `INCUS_SOCKET` | `/var/lib/incus/unix.socket` | Path to the Incus Unix socket |
| `SECRETS_BASE_PATH` | `/var/lib/svkexe/secrets` | Base path for materialized secret files |
| `SSH_ADDR` | `:2222` | SSH gateway listen address |
| `SSH_HOST_KEY_PATH` | `/var/lib/svkexe/ssh_host_key` | Path to persist the SSH host key (auto-generated if missing) |
| `RATE_LIMIT_RPS` | `10` | Per-user rate limit in requests per second |
| `RATE_LIMIT_BURST` | `20` | Per-user burst size (max tokens in bucket) |

## Security Checklist

- [ ] **GATEWAY_ENC_KEY** is set to a cryptographically random 32-byte hex value (`openssl rand -hex 32`)
- [ ] Caddy is configured to strip all `X-ExeDev-*` headers from incoming client requests (`request_header -X-ExeDev-*`)
- [ ] Authelia `forward_auth` is configured — unauthenticated requests must never reach the gateway
- [ ] Gateway is not exposed directly to the internet (sits behind Caddy)
- [ ] Incus socket permissions: gateway process user has read/write access to the Unix socket
- [ ] SSH host key is stored at `SSH_HOST_KEY_PATH` with mode `0600`
- [ ] SQLite database file has restrictive permissions (`0600`, owned by gateway user)
- [ ] `GATEWAY_ENC_KEY` is not committed to source control
- [ ] Rate limiting is tuned for your expected load (`RATE_LIMIT_RPS`, `RATE_LIMIT_BURST`)

## Caddy Configuration Example

```caddyfile
{
    email admin@yourdomain.com
}

*.yourdomain.com {
    # Strip client-supplied trust headers (Security Invariant S1)
    request_header -X-ExeDev-*

    forward_auth authelia:9091 {
        uri /api/authz/forward-auth
        copy_headers X-ExeDev-Userid X-ExeDev-Email
    }

    reverse_proxy gateway:8080
}
```

## Backup and Restore

### Backup

```bash
#!/bin/bash
# backup.sh — run daily via cron
BACKUP_DIR="/backups/$(date +%Y%m%d)"
mkdir -p "$BACKUP_DIR"

# 1. Backup gateway SQLite (WAL mode — safe to copy with .wal and .shm)
sqlite3 /var/lib/svkexe/gateway.db ".backup '$BACKUP_DIR/gateway.db'"

# 2. Snapshot all containers
for container in $(incus list --format csv -c n); do
    incus snapshot create "$container" "backup-$(date +%Y%m%d)"
done

echo "Backup complete: $BACKUP_DIR"
```

### Restore

```bash
#!/bin/bash
# restore.sh
BACKUP_DIR="/backups/20240101"

# 1. Restore gateway database
cp "$BACKUP_DIR/gateway.db" /var/lib/svkexe/gateway.db

# 2. Restore container snapshots
for container in $(incus list --format csv -c n); do
    incus restore "$container" "backup-20240101"
done
```

### RPO / RTO

| Metric | Value |
|---|---|
| RPO (Recovery Point Objective) | 24 hours (daily snapshots) |
| RTO (Recovery Time Objective) | < 30 minutes |

## Monitoring

The gateway exposes Prometheus-compatible metrics. A basic monitoring setup:

```yaml
# prometheus.yml scrape config
scrape_configs:
  - job_name: svkexe-gateway
    static_configs:
      - targets: ['gateway:8080']
    metrics_path: /metrics
```

Key metrics to alert on:
- `http_requests_total` — request rate and error rate
- `http_request_duration_seconds` — latency (alert on p95 > 5s)
- Container create/delete errors
- SQLite write failures

## Architecture Overview

```
┌─────────────────────────────────────────────────────┐
│                 Caddy (Reverse Proxy)                │
│     Wildcard TLS + Header Strip/Inject + Authelia    │
├─────────────────────────────────────────────────────┤
│              Go API Gateway (:8080)                  │
│   Auth middleware → Rate limiter → Route handlers    │
│   ContainerRuntime interface (Incus v1)              │
│   SQLite WAL (PRAGMA busy_timeout=5000)              │
├─────────────────────────────────────────────────────┤
│              Incus (LXC Containers)                  │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐           │
│  │ VM 1     │  │ VM 2     │  │ VM N     │           │
│  │ Shelley  │  │ Shelley  │  │ Shelley  │           │
│  │ :9000    │  │ :9000    │  │ :9000    │           │
│  └──────────┘  └──────────┘  └──────────┘           │
└─────────────────────────────────────────────────────┘
```

Auth chain:
```
Client → Caddy (strip X-ExeDev-*) → Authelia (forward_auth)
       → Caddy (inject verified headers) → Gateway (ownership check)
       → Rate limiter → Container:Shelley
```

See [PLAN.md](../PLAN.md) for full architecture decisions and phase roadmap.
