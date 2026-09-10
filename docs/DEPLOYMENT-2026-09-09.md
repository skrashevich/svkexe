# Deployment to 192.168.88.160 — 2026-09-09

## Installed

- Linux amd64 gateway: `/usr/local/bin/svkexe-gateway`, service `svkexe-gateway`.
- Agent: `/usr/local/lib/svkexe/picoclaw`, version `picoclaw-v0.3.1-svkexe`, customized build, Shelley shell commit `a305f7506d34a4783a174edf2d5e8ac97288d476`.
- Source including deployment fixes: `/opt/svkexe`.
- Base image `svkexe-base` updated from the existing developer image, preserving its tools. Fingerprint: `f3792360d390a82128afaea84e021ba6953ad78db1f3827f7a6aa255e891ac8c`.
- Agent LLM endpoint: `http://10.100.0.1:8080/api/llm/v1` on the private Incus bridge, protected by the existing internal bearer token. External TLS is not disabled in the agent.

## Backups

`/var/backups/svkexe/pre-picoclaw-20260909/` (root-only) contains the previous gateway binary, configuration, service unit, SQLite online backup, and source archive including pre-existing uncommitted changes.

Old image retained as `svkexe-base-pre-picoclaw-20260909`, fingerprint `9a438749d4b740530c318d79d6d6316790bafa048fc7ee4867bdd7516249e0f5`. No user VMs existed when deployment began.

## Live verification

| Path | Result |
| --- | --- |
| Login, authenticated API, admin listing, dashboard pages | Passed |
| API create from old Shelley image, first start and agent migration | Passed after fixing read-only key-file replacement |
| API create without explicit image | Passed with default PicoClaw image |
| Direct VM, `picoclaw.<vm>`, legacy `shelley.<vm>` gateway routing | Agent version, model list and HTML returned 200 |
| API recreate from new image | `/data` marker and conversation history preserved |
| Dashboard create/start/stop/recreate/delete | Passed; recreate preserved `/data` |
| SSH gateway shell | Real command executed in VM |
| SSH menu list/stat/whoami/stop/start/restart/rename/key list | Passed |
| SSH menu recreate | Passed; `/data` marker preserved |
| WebSocket terminal | Real command executed and returned over WebSocket |
| API key create/list/delete | Passed; plaintext absent from listing |
| SSH key creation and authentication | Passed with temporary key |
| Shared access including subsequent API calls and revocation | Passed |
| Anonymous agent API | Rejected with 401 |
| Agent history through gateway and restart/recreation | Preserved, including actual upstream error |
| Actual Linux binary agent → test gateway → simulated upstream → bash → streamed result → restart/history | Passed on the target server (`agent-smoke.log`) |
| Real OpenRouter completion | Blocked by workspace guardrails; no successful live-model completion claimed |
| Browser HTTPS at `svk.bar` | Blocked: `ERR_CERT_DATE_INVALID` |

Local verification: `go test -race ./...`, `go vet ./...`, focused regression tests for production fixes. The earlier agent migration also passed the preserved loop/server/database/provider suites and binary integration test. Production test artifacts/logs are in `/home/svk/svkexe-deploy-20260909/`.

## Fixes found by deployment testing

- Long VM operations no longer inherit the ordinary one-minute HTTP write timeout.
- Secret env files are atomically replaced without attempting to overwrite a mode-0400 file.
- Metrics middleware preserves HTTP hijacking, streaming and response-controller capabilities.
- Web terminal waits for the asynchronous runtime session to finish and drains output before closing.
- Shared grants supply a trusted agent identity and a host-only cookie; every request revalidates the grant, including after revocation.
- API creation defaults to `svkexe-base` when the image is omitted.
- Agent links use `<vm>.svk.bar`, covered by a normal `*.svk.bar` certificate.
- Rate-limit `Retry-After` uses numeric seconds rather than a date-format template.

## Remaining external issues

1. The externally served certificate for `svk.bar` / `*.svk.bar` expired on 2026-07-20. TLS terminates at external OpenResty, not on this server. Its renewal and external browser/SSE/WebSocket validation remain pending access to that proxy. Service-prefixed double-subdomain aliases need their own certificate coverage; ordinary dashboard links use the single-subdomain address.
2. The four previously configured free OpenRouter models were unavailable. They were replaced with currently listed free tool-capable models: `cohere/north-mini-code:free`, `nex-agi/nex-n2.5-mini:free`, `nvidia/nemotron-3.5-lightning:free`. All three are rejected by the workspace's existing model guardrails. Those rules were not bypassed or changed. Allow a working model in the workspace to enable real completions.
3. Existing Incus storage uses a `dir` pool whose backing filesystem does not support quotas; Incus logs that disk size enforcement is skipped. CPU/memory settings are applied, but disk limits are not enforced by this pre-existing storage setup.

## Final state

- Gateway is `active` and `enabled`; deployed SHA-256 matches the local release: `7b4a6583409474d0fbea1a4b73b427d54ebe01899f1310de284dd16e2ecace67`.
- Agent SHA-256: `f8faf8a55323039593c9b8d3d198307fa716bb6b48887d4493bc438099502b20`.
- Final checks additionally passed: metrics endpoint, actual rate limiting with numeric `Retry-After`, API stop/delete, SSH-menu deletion, dashboard single-subdomain links, logout/session invalidation.
- All temporary test VMs and the image-build container were deleted. The temporary SSH key was revoked and its private file removed; the test session was logged out. The new base image and the old rollback image remain.
- No successful real OpenRouter completion or valid public TLS browser session is claimed. These remain externally blocked as described above.
