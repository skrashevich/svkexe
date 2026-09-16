# PicoClaw agent with Shelley UI

A standalone coding-agent process: PicoClaw drives the LLM/tool loop, while Shelley provides the web UI, HTTP API, tools, streaming and SQLite conversation history. The compiled binary embeds the UI. It does not need the svkexe gateway, Incus, systemd or Node.js at runtime.

This directory is the complete source package. Copy it into another project, or export an archive with `make package`. The Go module in the parent repository is not used to build this package.

## Build

Build dependencies: Go with automatic toolchain selection (the pinned application requires Go 1.27.1), Git, Python 3, make, and Node.js/npm. The script bootstraps pinned Node 22.22.0 and pnpm 10.34.0. The initial build needs network access to fetch pinned sources, dependencies and the checksum-verified `exe-scroll` helper.

```sh
cd agent
make build                  # native binary: bin/picoclaw
make smoke                  # real process + deterministic local provider, no API key
make test                   # application/runtime compatibility tests
make package                # bin/picoclaw-agent-source.tar.gz
```

The source archive extracts to `agent/` and can be built outside this repository with the same commands. A binary distribution needs only `bin/picoclaw` and the license notices in `licenses/`; build tools and the generated source tree are not runtime dependencies. Coding tools execute programs installed on the target host, such as `bash` and `git`.

Build settings:

| Environment variable | Default | Meaning |
| --- | --- | --- |
| `AGENT_PROFILE` | `standalone` | `standalone` or `svkexe` |
| `AGENT_APP_NAME` | `PicoClaw` (`svkexe` for that profile) | UI title and web-app name |
| `AGENT_GOOS` | Host OS | Target OS, e.g. `linux` |
| `AGENT_GOARCH` | Host Go architecture | Target architecture, e.g. `amd64` |
| `AGENT_OUTPUT` | `agent/bin/picoclaw` | Binary destination |
| `AGENT_SOURCE_DIR` | `agent/bin/source-<profile>` | Generated source/cache directory |

Caller-supplied paths are resolved from the working directory. When using `make -C agent`, that directory is `agent/`. Native builds can run the smoke test; cross-compiled binaries must be tested on a matching host.

```sh
AGENT_APP_NAME='Project Assistant' make build
AGENT_GOOS=linux AGENT_GOARCH=amd64 AGENT_OUTPUT="$PWD/bin/picoclaw-linux-amd64" make build
```

## Run

From the project directory where the agent should work, run the binary using absolute state paths:

```sh
/path/to/agent/bin/picoclaw \
  -db /path/to/state/picoclaw.db \
  -disable-llm-integration -disable-gateway \
  serve -host 127.0.0.1 -port 9000 -socket none
```

Create the state directory first. Open `http://127.0.0.1:9000` to use the UI and configure models. Standalone builds bind loopback by default. Use `-host 0.0.0.0` only when the service should be reachable over the network. `-port 0 -port-file /path/to/port` lets a parent application discover an automatically allocated port.

Global flags go **before** `serve`; server flags go **after** it:

- `-db`: SQLite history and custom model configuration. Always set this explicitly; the inherited CLI default is `shelley.db`.
- `-config`: optional JSON configuration, for example `{"default_model":"custom-…"}`. Use the actual model ID returned by the API.
- `-default-model`: overrides the default model in that JSON file.
- `-disable-llm-integration -disable-gateway`: disables discovery of exe.dev-specific model integrations; direct providers and custom models remain available.
- `serve -host`, `-port`, `-socket`: listening interfaces. `-socket none` disables the separate local CLI socket.
- `serve -banner 'Project Assistant'`: optional visible banner.
- `serve -require-header X-Agent-User`: requires the hosting application's identity header for API requests.

The process has the filesystem and command-execution permissions of the account that runs it. For remote access, put it behind your application's authenticated reverse proxy. Header presence alone is not authentication: the proxy must strip client-supplied identity headers and inject its own after authentication. The agent is a workspace process, not a tenant-isolation boundary; run separate instances/state/workspaces for mutually untrusted users. The local CLI socket is a separate access path, so keep it disabled unless needed.

Project instructions use the retained `AGENTS.md` support. The optional `/etc/picoclaw/AGENTS.md` is an additional operator-provided instruction file; standalone operation does not require it.

## HTTP API

All examples use the loopback instance above. With `-require-header`, also provide the identity header through your authenticated proxy.

Create a model using any supported OpenAI-compatible endpoint:

```sh
curl -sS http://127.0.0.1:9000/api/custom-models \
  -H 'Content-Type: application/json' \
  -d '{"display_name":"My model","provider_type":"openai","endpoint":"http://127.0.0.1:8000/v1","api_key":"your-provider-key","model_name":"your-model","max_tokens":8192}'
```

Use the returned `model_id` to create a conversation:

```sh
curl -sS http://127.0.0.1:9000/api/conversations/new \
  -H 'Content-Type: application/json' \
  -d '{"message":"Inspect this project and explain its structure.","model":"MODEL_ID_FROM_PREVIOUS_RESPONSE","cwd":"/absolute/path/to/project"}'
```

The response contains `conversation_id`. Stream events or retrieve saved history:

```sh
curl -N http://127.0.0.1:9000/api/conversation/CONVERSATION_ID/stream
curl -sS http://127.0.0.1:9000/api/conversation/CONVERSATION_ID
curl -sS http://127.0.0.1:9000/api/models
curl -sS http://127.0.0.1:9000/version
```

The combined runtime retains Shelley's existing API and database schema. `/version` includes the `picoclaw-engine` capability. `tests/smoke.py` is an executable example that registers a model, runs a real bash tool, reads SSE and verifies history after restart, entirely without svkexe.

## State and updates

Keep the database, its SQLite sidecars, configuration and workspace outside the build directory. Back up state before upgrades. Stop the process, replace the binary with a new build from this package, then restart it with the same paths. Native Shelley self-updates are disabled because they would replace the combined PicoClaw runtime. The standalone version dialog explains this manual workflow.

The `svkexe` profile changes the default listen host to all interfaces, preserves the `picoclaw-…-svkexe` version name, and supplies the platform's version/update dialog. That dialog requires the gateway's authenticated `/version-check` and `/upgrade` handlers. The repository's `scripts/build-agent.sh` chooses this profile, defaults to Linux, and writes the existing `bin/picoclaw` artifact. VM management and platform configuration remain in `internal/picoclaw`; other projects do not need that package.

## Source layout and upstream maintenance

- `build.sh`, `Makefile`: independent build/test entrypoints.
- `upstream.env`, `go.mod.lock`, `go.sum.lock`: pinned application, runtime and dependency versions.
- `runtime.patch`: modifications to the pinned Shelley application, including loop delegation, provider fixes, the host flag and disabled upstream self-updates.
- `overlay/`: ordinary source templates added/replaced in the prepared upstream tree, including the PicoClaw adapter and standalone version dialog.
- `profiles/svkexe/`: platform-specific UI overlay.
- `configure.py`: applies overlays and safely encodes the selected application name.
- `tests/smoke.py`: independent real-process integration test.
- `licenses/`: Shelley (Apache-2.0) and PicoClaw (MIT) license notices.

Prepared sources under `bin/` are disposable; edit package inputs instead. `runtime.patch` is applied to an exact upstream commit, not to the latest branch. Updating that pin requires reviewing/rebasing the patch and running both compatibility and smoke tests. This packaging separates the agent from svkexe but retains the existing upstream-patch maintenance model.
