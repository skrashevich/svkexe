# svkexe
[![Go Report Card](https://img.shields.io/badge/go%20report-A%2B-brightgreen?style=flat&logo=go)](https://goreportcard.com/report/github.com/skrashevich/svkexe)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Go Reference](https://pkg.go.dev/badge/github.com/skrashevich/svkexe.svg)](https://pkg.go.dev/github.com/skrashevich/svkexe)
[![GitHub release](https://img.shields.io/github/v/release/skrashevich/svkexe?include_prereleases)](https://github.com/skrashevich/svkexe/releases)


Self-hosted platform for persistent Linux VMs with integrated [PicoClaw](https://github.com/sipeed/picoclaw) agent runtime, preserving the [Shelley](https://github.com/boldsoftware/shelley) web UI, coding prompts, tools and conversation storage. A self-hosted alternative to [exe.dev](https://exe.dev).

See [agent architecture, build and migration](docs/PICOCLAW.md) for the preserved contracts and existing-VM upgrade procedure.

## Features

- **Persistent Linux VMs** via Incus LXC containers with native systemd
- **PicoClaw coding agent with Shelley UI** per container — web-based, multi-conversation, multi-model AI assistant
- **Dynamic subdomain routing** — your workload at `https://{name}.yourdomain.com`, the agent at `https://agent-{name}.yourdomain.com`
- **WebSocket/SSE proxy** for real-time PicoClaw interactions
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
   | `OPENROUTER_API_KEY` | Your OpenRouter key (enables LLM proxy for PicoClaw) |

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

### From the dashboard (admin)

Admins get a **System** entry in the dashboard navigation (`/dashboard/system`). It shows the installed
versions of every component — gateway binary, its git commit and build date, the PicoClaw agent version
and the pinned Shelley commit — and offers two controls:

- **Check for updates** — asks the GitHub API whether `skrashevich/svkexe` has a newer build than the
  running binary, and links to the remote commit or release.
- **Update now** — starts the same `scripts/update.sh` run as the command line, with live progress and a
  log tail on the page.

The gateway restarts itself as part of the update, so the page briefly becomes unreachable and then
resumes polling on its own. Progress survives that restart because the update writes its state to
`/var/lib/svkexe/update-status.json` rather than reporting it over the HTTP request that started it.

**How the privileged part works.** The gateway service runs as the unprivileged `svkexe` user with
`NoNewPrivileges=true`, so it cannot elevate — and it cannot be the parent of a process that restarts it.
Instead it writes a trigger file, `/var/lib/svkexe/update.trigger`. A root-owned `svkexe-update.path`
systemd unit watches that file and starts the oneshot `svkexe-update.service`, which removes the trigger
and runs `scripts/update.sh`. Both units are installed by `scripts/install.sh`.

### From the command line

```bash
sudo /opt/svkexe/scripts/update.sh
```

Or remotely:

```bash
curl -fsSL https://raw.githubusercontent.com/skrashevich/svkexe/main/scripts/update.sh | sudo bash
```

The script pulls the latest code, rebuilds the gateway and PicoClaw agent, rebuilds the base image when agent/build sources change, and restarts the service. Running VM agents are migrated on gateway startup; stopped VMs migrate on their next start. Optional: `SVKEXE_BRANCH=...` (default: main), `SKIP_RESTART=1` (build only).

### Docker deployments

A container image is updated by pulling a new image, not by rebuilding in place, so the **Update now**
button reports the deployment as unable to self-update. Set `SVKEXE_UPDATE_COMMAND` to a command that
performs the update for your setup if you want the button to work there; the update check and the version
listing work regardless.

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
| `LLM_INTERNAL_TOKEN` | | Bearer token for PicoClaw → gateway auth |
| `LLM_PROXY_URL` | *(derived from DOMAIN)* | LLM proxy URL as seen from containers. If unset and DOMAIN is configured, defaults to `https://$DOMAIN/api/llm/v1` |

### Self-update

| Variable | Default | Description |
|---|---|---|
| `SVKEXE_UPDATE_OWNER` | `skrashevich` | GitHub account checked for new builds |
| `SVKEXE_UPDATE_REPO` | `svkexe` | GitHub repository checked for new builds |
| `SVKEXE_UPDATE_BRANCH` | `main` | Branch tracked by the `branch` channel |
| `SVKEXE_UPDATE_CHANNEL` | `branch` | `branch` (compare commit SHAs) or `release` (compare release tags; falls back to `branch` when the repo has no releases) |
| `SVKEXE_UPDATE_API_BASE` | `https://api.github.com` | GitHub API root |
| `SVKEXE_UPDATE_CACHE_TTL` | `15m` | How long an update check result is reused before hitting the API again |
| `SVKEXE_GITHUB_TOKEN` | *(falls back to `GITHUB_TOKEN`)* | Optional token; unauthenticated GitHub API calls are limited to 60/hour per IP |
| `SVKEXE_UPDATE_TRIGGER` | `/var/lib/svkexe/update.trigger` | File the gateway writes to request an update; watched by `svkexe-update.path` |
| `SVKEXE_UPDATE_STATUS` | `/var/lib/svkexe/update-status.json` | Machine-readable progress file written by `update.sh` and read by the dashboard |
| `SVKEXE_UPDATE_LOG` | `/var/lib/svkexe/update.log` | Full update log; the status file carries a bounded tail of it |
| `SVKEXE_UPDATE_COMMAND` | | Run this command directly instead of using the trigger file. For deployments where the gateway is already privileged (Docker, development) |

The gateway reports its own build metadata from ldflags stamped by `make`; a binary built with plain
`go build` reports version `dev` and the update check says it has no commit to compare against.

### User LLM endpoints

In **Dashboard → API Keys**, choose **OpenRouter** or **Custom OpenAI-compatible**.
OpenRouter uses `https://openrouter.ai/api/v1` by default. For custom connections,
enter a unique name (e.g. `local`), the complete API Base URL (e.g.
`http://10.0.0.10:8000/v1`), and comma-separated model IDs. The URL must be reachable
from the VM; `localhost` refers to that VM. Do not append `/chat/completions`.
A custom endpoint may omit its API key. Model IDs must match the provider exactly.

Multiple named custom connections can coexist. Save the same provider/name again
to replace its settings. Keys remain encrypted in the gateway database. Saving or
deleting settings reloads models and restarts the agent in running VMs; stopped
VMs receive changes on their next start. Sync failures are reported and can be
retried by restarting the VM. Select the resulting provider/model in the agent UI.

The REST API accepts the same settings at `POST /api/keys`, for example:

```json
{"provider":"custom-local","base_url":"http://10.0.0.10:8000/v1","models":"local-model","key":""}
```

User endpoints are independent of the gateway-wide `OPENROUTER_API_KEY` fallback.
When an owner has their own models, those are what a VM opens with and what the
initial task runs on; the `OPENROUTER_MODELS` list stays available in the VM as a
fallback for owners without keys.

### Initial task

When creating a VM you can describe, in plain text, what should be on it. The
text is handed to the agent as its first instruction once the VM is up, in a new
conversation, and runs with your own LLM key — so it starts spending your quota
right away. The agent treats it as a normal message, which means it may answer
or ask for clarification instead of building everything unattended.

Delivery happens once, and the VM card then follows the work itself:

| State | Means |
|---|---|
| `pending` | the VM is not up yet |
| `sent` | the agent accepted the task and is starting |
| `working` | the agent is running the task right now |
| `done` | the agent finished its turn without an error |
| `failed` | delivery failed, or the agent ended on an error — the reason is shown |

The gateway polls the agent every 15 seconds for as long as a task can still
change state, and stops once it is `done` or `failed`. A failure is most often
no configured model, which you fix by adding an LLM key and pressing Retry; a
failed task never retries by itself, so nothing fires unexpectedly on a later
restart. While a task is live the card links straight to its conversation in the
agent's own interface.

The REST API takes the same text as `initial_task` on `POST /api/containers`,
and `POST /api/containers/{id}/task/retry` re-queues a failed one.

### Workload routing

Each VM exposes two different things, on two separate hosts:

| URL | Serves | Who can reach it |
|---|---|---|
| `https://{name}.{domain}/` | your service, on the VM's configured port (default `3000`) | owner, share links, and anyone at all when the VM is public |
| `https://{port}-{name}.{domain}/` | your service on any other port | owner and share links only |
| `https://agent-{name}.{domain}/` | the PicoClaw web interface | the owner only |

Set the port and the Private/Public switch on the VM card in the dashboard, or
via `PUT /api/containers/{id}/publish` with `{"port":3000,"public":true}`.

**Public** serves the configured port to anonymous visitors — use it when the
service is meant to be public or does its own authentication. It applies to that
one port and nothing else: `{port}-{name}` hosts and the agent always require
your session. Switching back to Private closes access immediately.

The workload never receives `X-ExeDev-*` identity headers in either mode, so it
cannot mistake a gateway header for a signed-in user.

Because these hosts are single-label, a wildcard `*.{domain}` certificate covers
all of them. VM names therefore cannot start with `agent-` or a port prefix like
`3000-`.

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
│  │ PicoClaw │  │ PicoClaw │  │ PicoClaw │           │
│  │ :9000    │  │ :9000    │  │ :9000    │           │
│  └──────────┘  └──────────┘  └──────────┘           │
└─────────────────────────────────────────────────────┘
```

## Stack

| Component | Technology |
|---|---|
| VM Runtime | [Incus](https://linuxcontainers.org/incus/) (LXC) |
| Coding Agent | PicoClaw v0.3.1 runtime + pinned Shelley UI/prompts/tools |
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
  picoclaw/            Agent setup, migration, gateway models and backups
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
- The agent is not a multi-tenancy boundary — isolation is at the LXC container level
- LLM keys encrypted with AES-GCM, materialized as read-only tmpfs mounts
- Shared links scoped to specific containers with optional expiration

## Docs

- [Deployment Guide](docs/DEPLOY.md)
- [API Reference](docs/API.md)
- [Implementation Plan](PLAN.md)

## License

MIT
