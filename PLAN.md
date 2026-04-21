# Self-Hosted exe.dev — Implementation Plan v2

**Status:** APPROVED (Ralplan Consensus: Architect APPROVE + Critic APPROVE)
**Date:** 2026-04-21

## Overview

Self-hosted platform for persistent Linux VMs with integrated AI coding agent (Shelley). Replicates core exe.dev functionality for private deployment.

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                 Caddy (Reverse Proxy)                │
│     Wildcard TLS + Header Strip/Inject + Authelia    │
├─────────────────────────────────────────────────────┤
│              Go API Gateway                          │
│   user→container ownership check BEFORE proxying     │
│   ContainerRuntime interface (Incus v1)              │
│   SQLite WAL (PRAGMA busy_timeout=5000)              │
├─────────────────────────────────────────────────────┤
│              Incus (LXC Containers)                  │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐           │
│  │ VM 1     │  │ VM 2     │  │ VM N     │           │
│  │ Shelley  │  │ Shelley  │  │ Shelley  │           │
│  │ :9000    │  │ :9000    │  │ :9000    │           │
│  │ systemd  │  │ systemd  │  │ systemd  │           │
│  └──────────┘  └──────────┘  └──────────┘           │
└─────────────────────────────────────────────────────┘
```

### Components

| Component | Technology | Role |
|---|---|---|
| VM Runtime | Incus (LXC) | Persistent containers with native systemd |
| API Gateway | Go | User→container mapping, ownership enforcement, lifecycle via Incus REST API |
| Reverse Proxy | Caddy | Wildcard TLS, header strip/inject, Authelia forward_auth |
| Auth | Authelia (OIDC) | Authentication, session management |
| Dashboard | Go templates + htmx | VM management, key management, Web Shell |
| Coding Agent | Shelley (as-is) | Per-container, port 9000, Apache 2.0 |
| Database | SQLite (WAL) | Gateway state, user→container mapping |

### Auth Chain (v1)

```
Client → Caddy (strip X-ExeDev-*) → Authelia (forward_auth) → Caddy (inject verified headers) → Gateway (ownership check) → Container:Shelley
```

**exe-oidc-proxy:** deferred to v2 (not needed when auth is handled by Caddy+Authelia).

## Security Invariants

| # | Invariant | Enforcement |
|---|---|---|
| S1 | All incoming X-ExeDev-* headers stripped always | Caddy `request_header -X-ExeDev-*` |
| S2 | user_id→container_id mapping verified BEFORE proxying | Go gateway middleware |
| S3 | Gateway denies route to foreign workspace even with known hostname | Gateway ownership check |
| S4 | Shelley is NOT a trusted multi-tenancy boundary | Architectural decision, documented |
| S5 | LLM keys isolated per container via unique tmpfs mount paths | Read-only mount, unique path |

**Note:** Shelley's `RequireHeaderMiddleware` checks header **presence** only (not value), and only on `/api/*` routes. Security depends entirely on Caddy + Gateway correctness, not Shelley.

## Shelley Configuration Contract

```
Binary:    shelley (from github.com/boldsoftware/shelley, pinned version/SHA)
Flags:     -require-header X-ExeDev-Userid -port 9000 -db /data/shelley.db -systemd-activation
Env vars:  ANTHROPIC_API_KEY, OPENAI_API_KEY, GEMINI_API_KEY, FIREWORKS_API_KEY
systemd:   shelley.service with EnvironmentFile=/run/shelley/%i/env
Config:    shelley.json (optional, for future llm_gateway in v2)
```

## Secret Management Contract (v1)

```
Source of truth:  Gateway encrypted SQLite on host
Materialization:  /run/shelley/{container_id}/env (tmpfs, 0400, root:root) mounted read-only
                  EnvironmentFile= directive in shelley.service reads this into env vars
Rotation:         Dashboard → update gateway DB → regenerate env file → restart shelley.service
Revoke:           Delete env file + stop container
Isolation:        Unique path per container, mount namespace isolation
llm_gateway:      Deferred to v2 (centralized key proxy)
```

## Decision Matrix

| Criterion | Incus containers | Incus VMs | Docker (+s6) | Firecracker |
|---|---|---|---|---|
| systemd/cron | **Native** | Native | Via supervisor | Native |
| Isolation | cgroups+ns | Full KVM | cgroups+ns | Full KVM |
| Cold start | **~1s** | ~3-5s | <1s | ~125ms |
| Density (64GB) | **~100-150** | ~30-50 | ~150-200 | ~80-120 |
| Backup model | **Incus snapshots** | Incus snapshots | Volume backup | Custom |
| Ops complexity | **Low** | Medium | Low | High |
| "Feels like laptop" | **Yes** | Yes | Partial | Yes |

**Choice: Incus containers.** Hard requirement: Shelley `-systemd-activation` needs native systemd. Docker cannot provide this without hacks. Firecracker requires KVM bare metal. Incus VMs overkill for self-hosted single-user scenario.

## Principles

1. **Shelley-first** — use as-is, security boundary = LXC container, not Shelley
2. **GUTS Stack** — Go + Unix + TypeScript + SQLite
3. **Self-hosted simplicity** — Incus + Caddy + Authelia (3 external deps)
4. **Compatible headers** with mandatory stripping
5. **Secure by default** — formalized invariants S1-S5
6. **systemd-native workloads** — hard requirement for Shelley

## Phases

### Phase 0: Infrastructure (Week 1-2)

- Go monorepo: `cmd/gateway/`, `internal/`, `ui/`, `db/`
- Incus installation and configuration on host
- Base container image with pre-installed Shelley (pinned version)
- CI/CD pipeline (GitHub Actions)

**Exit criteria:** Monorepo compiles. Base image builds in <30s. `shelley --version` inside container returns correct response.

### Phase 1: API Gateway + Incus Integration (Week 2-4)

- Thin Go gateway with `ContainerRuntime` interface
- Incus implementation: create/start/stop/delete/snapshot via REST API (unix socket auth)
- User→container mapping in SQLite (WAL mode)
- Resource limits via Incus profiles (CPU, RAM, disk quotas)
- Persistent storage via Incus storage pools (ZFS/Btrfs)

**Exit criteria:** CRUD operations p95 <3s. Deleted container leaves no artifacts. Resource limits verified via `incus exec <c> -- nproc` and `/proc/meminfo`.

### Phase 2: Networking (Week 4-6)

- Caddy with wildcard TLS (Let's Encrypt DNS challenge)
- Dynamic routing: `{vm}.{domain}` → container IP:port
- Header stripping (all incoming X-ExeDev-*)
- WebSocket and SSE proxying
- Gateway graceful shutdown: drain WS/SSE, 503 on new requests

**Exit criteria:** Wildcard TLS works for `*.{domain}`. SSE streaming stable for 10 minutes without drops. WebSocket upgrade succeeds. Spoofed X-ExeDev-Userid header does not reach backend (tcpdump verification). Gateway returns 503 during shutdown.

### Phase 3: Auth (Week 6-7)

- Authelia OIDC configuration
- Caddy forward_auth integration
- Header injection after auth (X-ExeDev-Email, X-ExeDev-Userid)
- Per-VM access control (owner + shared links, Discord-style)
- Gateway ownership enforcement (S2, S3)

**Exit criteria:** Unauthenticated request → 401. User A → own container → 200. User A → container B → 403. Shared link grants access to specified container only.

### Phase 4: Shelley Integration (Week 7-9)

- Shelley configuration: flags, systemd unit with EnvironmentFile
- LLM key injection via tmpfs mount
- Auto-start via systemd-activation
- SSE streaming verification through full proxy chain
- Key isolation testing

**Exit criteria:** Shelley UI accessible at `https://{vm}.{domain}/`. Conversations create/list/delete work. LLM API call succeeds with platform key. User-override key applies after restart. Key user A not readable from container user B.

### Phase 5: Dashboard (Week 8-10)

- Go templates + htmx (minimal, not React SPA)
- VM list with statuses (running/stopped/creating)
- Create/delete/start/stop VM
- LLM API key management per-user
- Web Shell (xterm.js + WebSocket → ttyd)
- Quick-link to Shelley UI per VM

**Exit criteria:** VM list shows correct statuses. CRUD works via UI. Web Shell connects to container. Key management allows CRUD without cross-user leakage.

### Phase 6: SSH Gateway (Week 10-11)

- Go SSH server (gliderlabs/ssh)
- `ssh platform.domain` → interactive VM menu
- `ssh {vm}.platform.domain` → direct VM access
- SSH key management

**Exit criteria:** SSH menu displays user's VMs. SSH to specific VM works with authorized key. Unauthorized key → reject.

### Phase 7: Production Hardening (Week 11-12)

- Backup/restore via Incus snapshots + gateway SQLite backup
- Monitoring (Prometheus + Grafana)
- Rate limiting
- Network egress rules via Incus profiles (nftables)
- Disk quota alerts
- Deployment documentation
- Shelley update procedure

**Exit criteria:** Restore test: backup → clean environment → mapping + container + shelley.db restored and accessible. Load test: 50 concurrent containers on test server, <80% RAM, <60% CPU. 10 active Shelley sessions with SSE stable, p95 <5s.

## Verification Matrix

| Class | Test | Expected | Phase |
|---|---|---|---|
| Auth/Routing | Spoofed X-ExeDev-Userid header from client | Stripped, does not reach backend | 2 |
| Auth/Routing | User A requests container B | 403 Forbidden | 3 |
| Auth/Routing | Shared link to container A, try container B | 200 for A, 403 for B | 3 |
| Secret Isolation | Read /etc/shelley/env from container B | Permission denied / not found | 4 |
| Secret Isolation | Dashboard API user A → keys user B | 403 | 5 |
| Runtime Lifecycle | Create → use → stop → start → check data | Shelley conversations intact | 1, 4 |
| Runtime Lifecycle | Delete container → check artifacts | Clean Incus state, gateway DB updated | 1 |
| Data Durability | Backup → restore to clean env | Mapping, container, shelley.db intact | 7 |
| Data Durability | Concurrent write during backup | No corruption (SQLite WAL) | 7 |
| Capacity | 50 idle containers on test server | <80% RAM, <60% CPU | 7 |
| Capacity | 10 active Shelley sessions simultaneously | SSE stable, p95 response <5s | 7 |

## Capacity Profile

```
Per-container idle:    80-120MB RAM (systemd + Shelley Go process + SQLite)
Per-container active:  150-250MB RAM (Shelley + terminal + LLM streaming)
Target:               100 containers on 64GB RAM (640MB budget/container)
Overcommit:           Not required at >70% idle mix
Startup under load:   <3s (Incus + systemd) at 50% capacity
Disk per container:   ~500MB base image + user data (ZFS dedup reduces footprint)
```

## Backup/Restore Model

```
Backup unit:     gateway SQLite + Incus snapshot per container (includes shelley.db)
Consistency:     gateway DB backup → then Incus snapshots (ordered, not atomic)
RPO:             24h (daily snapshots, configurable)
RTO:             <30min (restore Incus snapshots + gateway DB)
Restore drill:   Automated script, runs monthly in CI
```

## ADR: Architecture Decision Record

**Decision:** Incus LXC containers + Caddy + Authelia + thin Go gateway + Shelley as-is

**Drivers:**
1. Shelley requires systemd-activation (hard requirement → excludes Docker)
2. Self-hosted simplicity (3 external deps, single-server deployment)
3. Shelley is production-ready coding agent (no need to build custom)

**Alternatives considered:**
- Firecracker microVMs: rejected (KVM required, high ops complexity)
- Docker + s6-overlay: rejected (no native systemd, breaks Shelley -systemd-activation)
- Incus VMs: rejected for v1 (lower density, overkill for self-hosted)
- Custom control plane: rejected (Incus REST API sufficient, saves 3-4 weeks)
- Full React SPA dashboard: rejected (Shelley UI is primary interface, htmx sufficient)

**Consequences:**
- Shared kernel between containers (acceptable for self-hosted, revisit for multi-tenant)
- Incus ecosystem smaller than Docker (less community tooling)
- ContainerRuntime interface allows future migration to Incus VMs or Firecracker

**Follow-ups (v2):**
- llm_gateway for centralized key management
- exe-oidc-proxy for OIDC pass-through in VMs
- Incus VM option for stronger isolation
- Container network egress policies
- Shelley update automation

## References

- [boldsoftware/shelley](https://github.com/boldsoftware/shelley) — Apache 2.0 coding agent
- [exe.dev docs](https://exe.dev/docs) — original platform documentation
- [Incus documentation](https://linuxcontainers.org/incus/docs/main/) — container runtime
- [Caddy](https://caddyserver.com/) — reverse proxy with auto-TLS
- [Authelia](https://www.authelia.com/) — OIDC authentication
