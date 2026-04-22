# svkexe

Self-hosted platform for persistent Linux VMs with integrated [Shelley](https://github.com/boldsoftware/shelley) AI coding agent. A self-hosted alternative to [exe.dev](https://exe.dev).

## Features

- **Persistent Linux VMs** via Incus LXC containers with native systemd
- **Shelley coding agent** per container — web-based, multi-conversation, multi-model AI assistant
- **Dynamic subdomain routing** — each VM accessible at `https://{name}.yourdomain.com`
- **WebSocket/SSE proxy** for real-time Shelley interactions
- **Web Shell** (xterm.js) for browser-based terminal access
- **SSH Gateway** with interactive VM menu and direct connect (`ssh vm@host`)
- **Shared links** (Discord-style) for temporary container access
- **LLM reverse proxy** to OpenRouter with automatic model fallback
- **LLM key management** with AES-GCM encryption, per-container isolation
- **Built-in web login** — cookie-based sessions, bcrypt password hashing, first-run admin bootstrap via env
- **Admin panel** for user and container management
- **Per-user rate limiting** (token bucket)
- **Prometheus metrics** + Grafana dashboards
- **Automated backups** — SQLite + Incus snapshots with 7-day retention

## Prerequisites

- **Linux host** — Ubuntu 24.04+ or Debian 12+ (amd64 or arm64)
- **Root (sudo) access** on the host
- **Domain with wildcard DNS** — you need an A record for the base domain and a wildcard record pointing to the same server:

  ```
  example.com      A    → 203.0.113.10
  *.example.com    A    → 203.0.113.10
  ```

  If you use Cloudflare, create both records in the DNS dashboard (the wildcard `*` record). Cloudflare proxy (orange cloud) works for the base domain but **not** for wildcard records on free plans — set the `*` record to "DNS only" (grey cloud).

## Installation

### Option A: One-liner install (bare metal, recommended)

The installer handles everything: system packages, Go, Docker, Incus, base container image, gateway binary, and systemd service.

1. **Run the installer** on a fresh server:

   ```bash
   curl -fsSL https://raw.githubusercontent.com/skrashevich/svkexe/main/scripts/install.sh | sudo bash
   ```

   Or with domain pre-filled:

   ```bash
   curl -fsSL https://raw.githubusercontent.com/skrashevich/svkexe/main/scripts/install.sh \
     | sudo env DOMAIN=example.com ACME_EMAIL=you@example.com bash
   ```

   If you already have a local checkout, run `sudo ./scripts/install.sh` directly — it auto-detects and uses the current tree.

2. **Review the generated config:**

   ```bash
   sudo $EDITOR /etc/svkexe/gateway.env
   ```

   The installer seeds this file with generated secrets and a random admin password. Key settings to check:

   | Variable | What to set |
   |---|---|
   | `DOMAIN` | Your base domain (e.g. `example.com`) |
   | `BOOTSTRAP_ADMIN_EMAIL` | Admin login email |
   | `BOOTSTRAP_ADMIN_PASSWORD` | Admin login password (printed during install, rotatable here) |
   | `GATEWAY_COOKIE_SECURE` | Set to `1` when behind HTTPS (Caddy or external TLS) |
   | `OPENROUTER_API_KEY` | Your OpenRouter key (enables LLM proxy for Shelley) |

3. **Start the service:**

   ```bash
   sudo systemctl start svkexe-gateway
   journalctl -u svkexe-gateway -f
   ```

4. **First login:** Open `http://<your-server>:8080/login` in a browser. Log in with the `BOOTSTRAP_ADMIN_EMAIL` and `BOOTSTRAP_ADMIN_PASSWORD` from the install output (also saved in `/etc/svkexe/gateway.env`). After login you'll see the dashboard where you can create your first VM.

The installer is idempotent — re-running is safe. Skip-flags for partial runs: `SKIP_DOCKER=1`, `SKIP_INCUS=1`, `SKIP_IMAGE_BUILD=1` (long step), `SKIP_GO=1`, `SKIP_BUILD=1`, `SKIP_SERVICE=1`. Override repo source with `SVKEXE_REPO=...`, `SVKEXE_BRANCH=...`, `SVKEXE_SRC_DIR=...`.

### Option B: Docker Compose

Use this if you want the full stack (Caddy for TLS + Authelia + Prometheus + Grafana) managed by Docker Compose. Incus still runs on the host.

1. **Clone and prepare Incus on the host:**

   ```bash
   git clone https://github.com/skrashevich/svkexe
   cd svkexe
   sudo ./scripts/setup-incus.sh
   sudo ./scripts/build-image.sh   # builds svkexe-base image (takes several minutes)
   ```

2. **Configure environment:**

   ```bash
   cd deploy
   cp docker-compose.yml docker-compose.override.yml
   ```

   Edit `docker-compose.override.yml` and set:

   | Variable | Service | Description |
   |---|---|---|
   | `DOMAIN` | caddy, gateway | Your base domain |
   | `ACME_EMAIL` | caddy | Email for Let's Encrypt certificates |
   | `CLOUDFLARE_API_TOKEN` | caddy | Cloudflare API token for DNS-01 challenge (TLS for wildcard domains) |
   | `AUTHELIA_SESSION_SECRET` | authelia | Random secret (`openssl rand -hex 32`) |
   | `ENC_KEY` | gateway | AES-256 key (`openssl rand -hex 32`) |

   > **Cloudflare API token:** Go to [Cloudflare API Tokens](https://dash.cloudflare.com/profile/api-tokens) → Create Token → use the "Edit zone DNS" template → select your zone. This token is required for Caddy to obtain wildcard TLS certificates via DNS-01 challenge.

3. **Launch:**

   ```bash
   docker compose up -d
   ```

4. **First login:** Open `https://yourdomain.com` — Authelia will handle authentication. Follow its first-run setup flow.

### Option C: Build from source

For development or custom deployments:

```bash
git clone https://github.com/skrashevich/svkexe
cd svkexe
make build

export GATEWAY_ENC_KEY="$(openssl rand -hex 32)"
export DOMAIN="yourdomain.com"
export GATEWAY_DB_PATH="/var/lib/svkexe/gateway.db"
export BOOTSTRAP_ADMIN_EMAIL="admin@example.com"
export BOOTSTRAP_ADMIN_PASSWORD="changeme"

./bin/gateway
```

Requires a running Incus daemon with the `svkexe-base` image (see `scripts/setup-incus.sh` and `scripts/build-image.sh`).

## Update

To update a running instance to the latest version:

```bash
sudo /opt/svkexe/scripts/update.sh
```

Or remotely:

```bash
curl -fsSL https://raw.githubusercontent.com/skrashevich/svkexe/main/scripts/update.sh | sudo bash
```

The script pulls the latest code, rebuilds the binary, rebuilds the base image if `build-image.sh` changed, and restarts the service. Optional: `SVKEXE_BRANCH=...` (default: main), `SKIP_RESTART=1` (build only).

## Configuration

All configuration is via environment variables. For bare-metal installs, edit `/etc/svkexe/gateway.env`.

| Variable | Default | Description |
|---|---|---|
| `GATEWAY_ADDR` | `:8080` | HTTP listen address |
| `GATEWAY_DB_PATH` | `/var/lib/svkexe/gateway.db` | SQLite database path |
| `GATEWAY_ENC_KEY` | | AES-256 key for API key encryption (hex, `openssl rand -hex 32`) |
| `GATEWAY_COOKIE_SECURE` | `0` | Set to `1` when served over HTTPS |
| `DOMAIN` | | Base domain for subdomain routing |
| `INCUS_SOCKET` | `/var/lib/incus/unix.socket` | Incus API socket |
| `SSH_ADDR` | `:2222` | SSH gateway listen address |
| `SSH_HOST_KEY_PATH` | `/var/lib/svkexe/ssh_host_key` | ED25519 host key (auto-generated if missing) |
| `SECRETS_BASE_PATH` | `/var/lib/svkexe/secrets` | Key materialization directory |
| `RATE_LIMIT_RPS` | `10` | Requests per second per user |
| `RATE_LIMIT_BURST` | `20` | Burst size |
| `BOOTSTRAP_ADMIN_EMAIL` | | Admin account email (created/updated on startup) |
| `BOOTSTRAP_ADMIN_PASSWORD` | | Admin account password (re-hashed on every restart — rotate by changing this value) |
| `OPENROUTER_API_KEY` | | OpenRouter API key (enables LLM proxy) |
| `OPENROUTER_MODELS` | `anthropic/claude-sonnet-4,openai/gpt-4o,google/gemini-2.5-flash` | Models to try in order (comma-separated) |
| `LLM_INTERNAL_TOKEN` | | Bearer token for Shelley → gateway auth |
| `LLM_PROXY_URL` | *(derived from DOMAIN)* | LLM proxy URL as seen from containers. If unset and DOMAIN is configured, defaults to `https://$DOMAIN/api/llm/v1` |

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
  llmproxy/            LLM reverse proxy to OpenRouter with model fallback
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

Common user-facing endpoints (see [docs/API.md](docs/API.md) for the full reference including admin routes):

```
GET    /api/containers              List user's containers
POST   /api/containers              Create container
GET    /api/containers/{id}         Get container details
POST   /api/containers/{id}/start   Start container
POST   /api/containers/{id}/stop    Stop container
POST   /api/containers/{id}/recreate Recreate container (backup data → rebuild → restore)
DELETE /api/containers/{id}         Delete container
POST   /api/containers/{id}/share   Create shared link
GET    /api/containers/{id}/shares  List shared links
DELETE /api/shares/{token}          Revoke shared link
GET    /api/keys                    List LLM API keys
POST   /api/keys                    Create LLM API key
DELETE /api/keys/{id}               Revoke LLM API key
GET    /api/ssh-keys                List SSH keys
POST   /api/ssh-keys                Add SSH key
DELETE /api/ssh-keys/{id}           Remove SSH key
GET    /api/me                      Current user info
POST   /api/llm/v1/chat/completions LLM proxy (OpenRouter)
GET    /api/llm/v1/models           List available models
GET    /metrics                     Prometheus metrics (unauthenticated)
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
