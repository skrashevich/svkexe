# svkexe

Self-hosted platform for persistent Linux VMs with integrated [Shelley](https://github.com/boldsoftware/shelley) AI coding agent. A self-hosted alternative to [exe.dev](https://exe.dev).

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                 Caddy (Reverse Proxy)                │
│     Wildcard TLS + Header Strip/Inject + Authelia    │
├─────────────────────────────────────────────────────┤
│              Go API Gateway (:8080)                  │
│   Ownership enforcement, rate limiting, Prometheus   │
├─────────────────────────────────────────────────────┤
│   SSH Gateway (:2222)    │    htmx Dashboard         │
│   Interactive VM menu    │    VM / Keys / Shell       │
├──────────────────────────┴──────────────────────────┤
│              Incus (LXC Containers)                  │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐           │
│  │ VM 1     │  │ VM 2     │  │ VM N     │           │
│  │ Shelley  │  │ Shelley  │  │ Shelley  │           │
│  │ :9000    │  │ :9000    │  │ :9000    │           │
│  └──────────┘  └──────────┘  └──────────┘           │
└─────────────────────────────────────────────────────┘
```

## Features

- **Persistent Linux VMs** via Incus LXC containers with native systemd
- **Shelley coding agent** per container — web-based, multi-conversation, multi-model AI assistant
- **Dynamic subdomain routing** — each VM accessible at `https://{name}.yourdomain.com`
- **WebSocket/SSE proxy** for real-time Shelley interactions
- **Web Shell** (xterm.js) for browser-based terminal access
- **SSH Gateway** with interactive VM menu and direct connect (`ssh vm@host`)
- **Shared links** (Discord-style) for temporary container access
- **LLM key management** with AES-GCM encryption, per-container isolation
- **Built-in web login** — cookie-based sessions, bcrypt password hashing, first-run admin bootstrap via env
- **Admin panel** for user and container management
- **Per-user rate limiting** (token bucket)
- **Prometheus metrics** + Grafana dashboards
- **Automated backups** — SQLite + Incus snapshots with 7-day retention

## Quick Start

### Prerequisites

- Linux host (Ubuntu 24.04+ / Debian 12+ recommended, amd64 or arm64)
- Domain with wildcard DNS (`*.yourdomain.com`)
- Root (sudo) access on the host

### One-liner install (recommended)

On a fresh Ubuntu 24.04+ / Debian 12+ server:

```bash
curl -fsSL https://raw.githubusercontent.com/skrashevich/svkexe/main/scripts/install.sh | sudo bash
```

Or with production defaults pre-filled in `/etc/svkexe/gateway.env`:

```bash
curl -fsSL https://raw.githubusercontent.com/skrashevich/svkexe/main/scripts/install.sh \
  | sudo env DOMAIN=example.com ACME_EMAIL=you@example.com bash
```

The installer clones the repo to `/opt/svkexe`, then installs system packages,
Go, Docker + Compose, Incus, runs host setup, builds the `svkexe-base` Incus
image, builds the `gateway` binary, and installs a hardened
`svkexe-gateway.service` systemd unit. It is idempotent — re-running is safe.

After it finishes:

```bash
sudo $EDITOR /etc/svkexe/gateway.env       # review DOMAIN, ACME_EMAIL, etc.
sudo systemctl start svkexe-gateway
journalctl -u svkexe-gateway -f
```

If you already have a local checkout you can run `sudo ./scripts/install.sh`
directly — it auto-detects and uses the current tree instead of cloning.

Useful skip-flags (comma-combine via env): `SKIP_DOCKER=1`, `SKIP_INCUS=1`,
`SKIP_IMAGE_BUILD=1` (long), `SKIP_GO=1`, `SKIP_BUILD=1`, `SKIP_SERVICE=1`.
Override repo source with `SVKEXE_REPO=...`, `SVKEXE_BRANCH=...`,
`SVKEXE_SRC_DIR=...`.

### Deploy with Docker Compose

```bash
git clone https://github.com/skrashevich/svkexe
cd svkexe

# Set up Incus on the host
sudo ./scripts/setup-incus.sh

# Build the Shelley base container image
sudo ./scripts/build-image.sh

# Configure environment
cd deploy
cp docker-compose.yml docker-compose.override.yml
# Edit docker-compose.override.yml with your settings:
#   DOMAIN, GATEWAY_ENC_KEY, INCUS_SOCKET, etc.

# Launch
docker compose up -d
```

### Build from Source

```bash
go build -o bin/gateway ./cmd/gateway

export GATEWAY_ENC_KEY="$(openssl rand -hex 32)"
export DOMAIN="yourdomain.com"
export GATEWAY_DB_PATH="/var/lib/svkexe/gateway.db"

./bin/gateway
```

## Configuration

| Variable | Default | Description |
|---|---|---|
| `GATEWAY_ADDR` | `:8080` | HTTP listen address |
| `GATEWAY_DB_PATH` | `/var/lib/svkexe/gateway.db` | SQLite database path |
| `GATEWAY_ENC_KEY` | | AES-256 key for API key encryption (hex) |
| `DOMAIN` | | Base domain for subdomain routing |
| `INCUS_SOCKET` | `/var/lib/incus/unix.socket` | Incus API socket |
| `SSH_ADDR` | `:2222` | SSH gateway listen address |
| `SSH_HOST_KEY_PATH` | `/var/lib/svkexe/ssh_host_key` | ED25519 host key (auto-generated) |
| `SECRETS_BASE_PATH` | `/run/shelley` | Key materialization directory |
| `RATE_LIMIT_RPS` | `10` | Requests per second per user |
| `RATE_LIMIT_BURST` | `20` | Burst size |

## Stack

| Component | Technology |
|---|---|
| VM Runtime | [Incus](https://linuxcontainers.org/incus/) (LXC) |
| Coding Agent | [Shelley](https://github.com/boldsoftware/shelley) (Go + React) |
| API Gateway | Go, [chi](https://github.com/go-chi/chi), SQLite (WAL) |
| Reverse Proxy | [Caddy](https://caddyserver.com/) |
| Auth | Built-in web login (bcrypt + session cookies) |
| Dashboard | Go templates + [htmx](https://htmx.org/) |
| Web Shell | [xterm.js](https://xtermjs.org/) + WebSocket |
| SSH Gateway | [gliderlabs/ssh](https://github.com/gliderlabs/ssh) |
| Monitoring | [Prometheus](https://prometheus.io/) + [Grafana](https://grafana.com/) |

## Project Structure

```
cmd/gateway/           Entry point (HTTP + SSH servers)
internal/
  api/                 REST API, middleware, admin endpoints
  dashboard/           htmx pages (VMs, Keys, SSH Keys, Shell)
  db/                  SQLite with WAL, all CRUD operations
  proxy/               Dynamic reverse proxy (WebSocket/SSE)
  runtime/             ContainerRuntime + ShellRuntime interfaces
  secrets/             LLM key materialization (encrypted DB -> env file)
  shelley/             Shelley config contract + container setup
  sshgw/               SSH gateway with interactive menu
  metrics/             Prometheus metrics + middleware
  ratelimit/           Per-user token bucket rate limiter
ui/templates/          Go HTML templates
deploy/                Caddy, Authelia, Docker Compose, Prometheus
scripts/               Host setup, image build, backup/restore
docs/                  Deployment guide, API reference
```

## API

Full API reference: [docs/API.md](docs/API.md)

```
GET    /api/containers              List user's containers
POST   /api/containers              Create container
POST   /api/containers/{id}/start   Start container
POST   /api/containers/{id}/stop    Stop container
DELETE /api/containers/{id}         Delete container
GET    /api/keys                    List LLM API keys
POST   /api/containers/{id}/share   Create shared link
GET    /api/me                      Current user info
GET    /metrics                     Prometheus metrics
```

## Security

- All incoming `X-ExeDev-*` headers stripped by Caddy before auth
- User-to-container ownership verified before every proxy request
- Shelley is not a multi-tenancy boundary — isolation is at the LXC container level
- LLM keys encrypted with AES-GCM, materialized as read-only tmpfs mounts
- Shared links scoped to specific containers with optional expiration

## Docs

- [Deployment Guide](docs/DEPLOY.md)
- [API Reference](docs/API.md)
- [Implementation Plan](PLAN.md)

## License

MIT
