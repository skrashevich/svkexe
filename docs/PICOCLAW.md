# PicoClaw agent in svkexe

Each VM runs `/usr/local/bin/picoclaw` on port 9000. This is svkexe's integration build: **PicoClaw v0.3.1 owns the LLM/tool iteration loop and parallel dispatch**, while the pinned Shelley application provides the coding prompts, tool implementations, web interface, streaming, models, CLI client and SQLite storage. It is not the standalone PicoClaw `gateway` CLI. The runtime is shared by every conversation, including CLI and subagent conversations.

## Preserved contracts

- The complete Shelley web application: conversations, history, model selection, terminal, file/tool widgets, streaming, cancellation, retry, hooks and coding tools.
- The original rich prompt/history representation, including images, reasoning blocks, tool IDs, usage accounting and ordered tool-result persistence. A provider adapter avoids converting these into plain text.
- The existing application API and database schema. Conversation history survives the upgrade; only the file names change (see below).
- Guest state lives at `/data/picoclaw.db`, `/etc/picoclaw/picoclaw.json` and `/etc/picoclaw/env`. Pre-rename VMs carried `/data/shelley.db` and `/etc/shelley/`; setup moves them, including any `-wal`/`-shm` sidecars, and the move is conditional so repeated setups stay idempotent.
- `https://<vm>.<domain>/`, `https://picoclaw.<vm>.<domain>/` and old `https://shelley.<vm>.<domain>/` links use the same ownership-checked proxy.
- The dashboard uses `<vm>.<domain>` so a normal `*.<domain>` TLS certificate covers agent links. Service-prefixed aliases require additional certificate coverage at the external reverse proxy.
- The former `/usr/local/bin/shelley` symlink and `shelley.service` alias are removed during migration, so the guest exposes only the name that actually runs.
- Provider keys configured by the user still use the retained model adapters. The gateway's custom models use the exact `/api/llm/v1` endpoint and internal bearer token. `llm_gateway` is deliberately not set: that Shelley setting expects exe.dev's provider-specific API.

The adapter lives in `agent/overlay/loop/picoclaw.go.in`; the narrowly scoped changes to the preserved application are in `agent/runtime.patch`. Its private per-call dispatch identifiers preserve raw JSON arguments and original tool-use IDs; those identifiers are never exposed to the model or the UI. The agent advertises `picoclaw-engine` in `/version`. Upstream self-updates are disabled by marking the binary customized, preventing an accidental replacement with the Shelley engine.

## Build and checks

Requirements: Go with automatic toolchain selection (the pinned shell needs Go 1.27.1), Node.js/npm (used to bootstrap pinned Node 22.22.0 and pnpm), Python 3, git and make. `scripts/build-agent.sh` pins the upstream source SHA, PicoClaw dependency, module checksums and pnpm version. No `latest` agent release is downloaded. Node is a build dependency only; the resulting agent embeds the UI.

```sh
make build                    # bin/gateway + bin/picoclaw (Linux, host CPU architecture)
make gateway                  # gateway only
make agent                    # agent only
make test                     # svkexe tests
make test-agent               # retained shell/API/DB/provider/loop tests
AGENT_GOARCH=amd64 make agent  # Linux amd64 agent, e.g. cross-build on Apple Silicon
```

Generated sources/assets live under ignored `bin/agent-source`. To run the real end-to-end test on macOS, build a native test binary separately:

```sh
AGENT_GOOS=darwin AGENT_GOARCH=arm64 AGENT_OUTPUT=/tmp/picoclaw-test make agent
SVKEXE_TEST_AGENT_BINARY=/tmp/picoclaw-test go test ./internal/llmproxy -run TestPicoClawThroughGateway -v
```

The integration test launches the real agent server and verifies its UI/API, identity header, PicoClaw capability, authenticated svkexe LLM proxy, real bash execution, SSE response and history after process restart. Its upstream model is deterministic; it does not consume an external API key. CI runs this test with the Linux binary.

## Install and migrate

`scripts/install.sh`, `scripts/update.sh` and the Docker build install the agent at `/usr/local/lib/svkexe/picoclaw` alongside the gateway. `SVKEXE_AGENT_BINARY` can select an explicit host artifact. The binary must target Linux and the guest's CPU architecture. Development gateway builds also look for `picoclaw` beside the gateway binary. New images contain the agent; an old image requires the host artifact before its service is changed.

On gateway startup, existing running VMs are reconciled. This restarts their agent services, so finish active agent work before updating. Stopped VMs migrate at their next API/dashboard/SSH start. Setup is serialized per VM and bounded by a timeout:

1. Verify/install the agent artifact, then stop the old service.
2. Save a one-time SQLite backup at `/data/picoclaw.pre-rename.db`, then move pre-rename database and credentials to the PicoClaw paths.
3. Write config/keys with restrictive permissions and install `picoclaw.service`.
4. Start the agent to initialize/migrate the database; seed current gateway models and token; restart to load them.
5. Require successful HTTP `/api/models` readiness before reporting success.

Create, start, restart and recreate paths use the same setup. VM recreation stops the agent before backing up `/data`; a backup failure aborts deletion. Restore finishes before starting the new agent, after which current credentials/models are applied. API/dashboard/SSH surface setup failures instead of reporting the agent ready. User-key changes take effect on the next agent setup/start, as before.

## Rollback

Keep a VM snapshot or the recreation archive before deployment. To roll back, stop the new agent, reinstall the previous gateway/image/agent and restore the pre-upgrade SQLite database at its original `/data/shelley.db` path with the service stopped (also remove any newer `picoclaw.db-wal`/`picoclaw.db-shm`). The one-time `/data/picoclaw.pre-rename.db` contains history up to the first migration, not conversations created afterward. Do not point an older Shelley binary at a database already migrated by a newer application without restoring its backup.

## Attribution

The preserved Shelley source is pinned to `a305f7506d34a4783a174edf2d5e8ac97288d476` from [boldsoftware/shelley](https://github.com/boldsoftware/shelley), under Apache-2.0. The runtime uses [sipeed/picoclaw v0.3.1](https://github.com/sipeed/picoclaw/tree/v0.3.1), under MIT. License copies are in `agent/licenses/`.

## Local verification (2026-09-09)

- Built the gateway, Linux arm64 agent and native macOS arm64 test agent.
- Passed `go test -race ./...` and `go vet ./...` for svkexe.
- Passed the preserved `loop`, `server`, `modelsources`, `db` and `llm/...` suites; also passed `go test -race ./loop`.
- Passed the real-binary gateway integration test (bash tool, authentication, SSE, restart/history).
- Verified the embedded web application in a browser with the deterministic model.
- Passed shell syntax checks and `git diff --check`.

Incus is unavailable in the local development environment. Building/publishing the Incus image, exercising systemd inside real VMs, deployment, and calls to a live OpenRouter model have not been performed here. Container setup and SSH recreation are covered by runtime contract tests; the complete agent/gateway HTTP path is covered by the real-binary test with a deterministic upstream.
