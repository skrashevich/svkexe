# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

**svkexe** — self-hosted platform for persistent Linux VMs with integrated PicoClaw runtime with Shelley UI and coding prompts. Provides multi-tenant VM management, web shell, SSH gateway, and LLM proxy over Incus containers.

- **Module:** `github.com/skrashevich/svkexe`
- **Go version:** 1.26.2
- **Single binary:** `cmd/gateway/main.go` — HTTP server (:8080) + SSH gateway (:2222)

## Commands

```bash
make build          # CGO_ENABLED=0, output → ./bin/gateway
make test           # go test ./...
make run            # build + run
make clean          # rm ./bin + go clean

# Single package test
go test ./internal/api
go test -run TestLoginPost ./internal/api
```

## Architecture

```
Caddy (TLS, header strip) → Go Gateway (:8080) → Incus containers (LXC)
                                 ↓
              ┌──────────┬───────────┬──────────────┐
              │ API/REST │ Dashboard │ SSH GW :2222 │
              │ (chi)    │ (htmx)    │ (gliderlabs) │
              └──────────┴───────────┴──────────────┘
```

**Request routing:** `buildTopHandler` in `main.go` splits by Host header — subdomain requests (`*.DOMAIN`) go to container proxy (`internal/proxy`), everything else to the API server (`internal/api`).

### Key packages

| Package | Role |
|---------|------|
| `internal/api` | REST API, chi router, auth/session middleware, admin endpoints |
| `internal/db` | SQLite (WAL mode, pure-Go driver `modernc.org/sqlite`), embedded schema + idempotent migrations |
| `internal/runtime` | `ContainerRuntime` interface; `IncusRuntime` implementation |
| `internal/proxy` | Dynamic HTTP/WebSocket reverse proxy to containers, ownership-gated |
| `internal/dashboard` | htmx UI — VM/keys/shell management |
| `internal/sshgw` | SSH gateway with interactive TUI menu for VM selection |
| `internal/llmproxy` | OpenRouter proxy with model fallback chain |
| `internal/metadata` | EC2-compatible instance metadata service (IMDSv1+v2) served to VMs on `169.254.169.254:80`, plus the guest route helper it installs |
| `internal/secrets` | AES-256 encryption of LLM keys, materialization to tmpfs |
| `internal/picoclaw` | PicoClaw setup, migration and retained application contract |
| `internal/aliases` | Custom-domain lifecycle: claim, DNS verification, agent guide refresh |
| `internal/dnscheck` | Verifies that a custom domain resolves to this gateway before it is routed |
| `internal/ratelimit` | Per-user token bucket rate limiter |
| `internal/vmconfig` | Resolves per-VM runtime settings (nested containers) against the deployment-wide ceiling and writes them to Incus |
| `internal/auth` | Session management, bcrypt (cost 12) |
| `internal/version` | Build metadata stamped in via `-ldflags -X` from the Makefile |
| `internal/updater` | GitHub update check + trigger-file handoff to the root-owned `svkexe-update` systemd units |

### UI layer

- `ui/` — embedded Go templates + static assets (`ui/templates/`, `ui/ui.go`)
- `index.html` — landing page (standalone)
- Dashboard uses htmx for interactivity

## Configuration

All configuration is via environment variables (no config files). Key vars:

- `DOMAIN` — base domain for subdomain routing (required in production)
- `GATEWAY_ENC_KEY` — 32-byte hex AES-256 key for LLM secret encryption
- `GATEWAY_DB_PATH` — SQLite path (default `/var/lib/svkexe/gateway.db`)
- `INCUS_SOCKET` — Incus Unix socket (default `/var/lib/incus/unix.socket`)
- `METADATA_ADDR` — where the gateway listens for instance metadata requests (default `10.100.0.1:8081`, the bridge address; VMs reach it at `169.254.169.254:80` via a host redirect; `off` disables it)
- `BOOTSTRAP_ADMIN_EMAIL` / `BOOTSTRAP_ADMIN_PASSWORD` — idempotent admin account setup

## Patterns

- **Ownership enforcement:** Every container operation checks `user_id → container_id` mapping before proceeding (`OwnershipMiddleware`)
- **Container naming:** `svkexe-{owner_id}-{name}` — encodes ownership at Incus level
- **DB migrations:** Embedded SQL schema + idempotent `ALTER TABLE` in `db.migrate()`; check `columnExists()` before adding columns
- **Nested containers:** `security.nesting` is written onto every instance the gateway creates or starts, never left to the Incus profile. `containers.nesting` is the owner's wish, the `nesting_allowed` row in `settings` is the operator's ceiling, and `containers.nesting_applied` is what the running instance booted with — their difference is what makes a card ask for a restart. Every path that starts a VM must call `vmconfig.PrepareStart`; every path that builds one passes `CreateOpts.Nesting` and calls `vmconfig.MarkStarted`.
- **Metadata identity comes from live runtime state, and only where the address is pinned:** the metadata service attributes a request to a VM by asking Incus which instance currently holds the source address, then loading that instance's row by `incus_name`. Never resolve it through `containers.ip_address` — that column is only refreshed when something happens to a VM, so a reassigned address would hand the new holder somebody else's identity. The source address is only trustworthy because `security.ipv4_filtering` is set on the `svkexe-default` NIC; a VM whose NIC is unfiltered is refused (`RuntimeResolver.addressIsPinned`), because a tenant is root in their own VM and could otherwise claim a neighbour's address. Nothing secret may enter a `metadata.Identity`; the whole tree is served unauthenticated to every process in the VM.
- **The host never holds `169.254.169.254`:** it redirects bridge traffic for it to the gateway's unprivileged port. Taking the address would route the *host's* own traffic to it locally, which on any cloud VPS breaks the host's access to its provider's metadata service at the same address.
- **Dependency injection:** Constructors accept all deps (`NewServer(db, rt, encKey, domain, ...)`)
- **Error wrapping:** `fmt.Errorf("context: %w", err)` throughout
- **Tests:** Table-driven, real SQLite (in-memory), `httptest.Server` for integration; no mocking framework

## Deployment

- **Bare metal:** `scripts/install.sh` (Ubuntu 24.04+/Debian 12+)
- **Docker:** `deploy/docker-compose.yml` (Caddy + Authelia + Gateway + Prometheus + Grafana)
- **Update:** `scripts/update.sh`
