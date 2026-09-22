---
title: API reference
description: REST endpoints of the svkexe gateway
---

# API Reference

## Authentication

Management endpoints use the `svkexe_session` cookie from `POST /login`.
Client-supplied `X-ExeDev-*` headers do not authenticate a request, and LLM keys
stored by `POST /api/keys` are provider credentials, not management API tokens.

Use your own gateway domain (replace `example.com`):

```bash
curl -c cookies.txt https://example.com/login \
  --data-urlencode 'email=admin@example.com' \
  --data-urlencode 'password=your-password'
curl -b cookies.txt https://example.com/api/me
```

Send the cookie on subsequent requests, and `Content-Type: application/json`
with JSON bodies. Login succeeds with `303 See Other`; an API request without a
valid session returns `401`. `POST /logout` invalidates the session.
`GET/POST /register` only creates the first admin while no accounts exist.

Exceptions: `/metrics` and `/api/tls/check` are public. The configured LLM proxy
at `/api/llm/v1/*` uses its own internal Bearer token, as described
below. Guest accounts can read assigned VMs and manage their SSH keys but cannot
manage VM lifecycle or LLM connections; see [ACCESS.md](ACCESS.md).

JSON field names are case-sensitive. Container objects currently serialize Go
field names (`ID`, `Name`, `OwnerID`, `Status`, `Nesting`, `NestingApplied`,
`NestingAllowed`, `SharedAccess`, etc.). Request bodies use snake_case. Examples
below show selected response fields, not an exhaustive schema.
---

## Endpoints

### Current User

#### `GET /api/me`

Returns the authenticated user's profile.

**Response `200 OK`:**
```json
{
  "id": "user-uuid",
  "email": "user@example.com"
}
```

---

### Containers

#### `GET /api/containers`

List all containers owned by or assigned to the authenticated user. Assigned
VMs have `SharedAccess: true`; this does not grant management rights.

**Response `200 OK`:**
```json
[
  {
    "ID": "container-uuid",
    "Name": "my-vm",
    "Status": "running",
    "CreatedAt": "2024-01-01T00:00:00Z"
  }
]
```

---

#### `POST /api/containers`

Create a new container. The container is started and its agent configured
before the response is written, so this call takes as long as the VM needs to
come up — there is no separate start step.

**Request body:**
```json
{
  "name": "my-vm",
  "nesting": true
}
```

Optional creation fields are `image` (default `svkexe-base`), `cpu_limit`
(default 2), `memory_mb` (2048), `disk_gb` (10), and `initial_task` (up to 4000
characters, delivered to the agent after startup). Names use lowercase letters,
digits and hyphens, 2–63 characters; reserved agent/port prefixes are rejected.

`nesting` decides whether the VM may run containers of its own — Docker, buildah,
a nested Incus. Omitting it enables them, which is the platform default; send
`false` to opt out. The deployment-wide setting under **System → Nested
containers** is a ceiling: while an admin has it off, `true` here is stored as
the owner's wish but no VM gets the capability.

This is the only REST surface for the setting: changing it on an existing VM is
done from the dashboard, which also offers the restart the change needs. Reads
(`GET /api/containers`, `GET /api/containers/{id}`) report `Nesting` — what the
owner asked for — alongside `NestingApplied`, what the running instance actually
booted with. The two differing on a running VM means a restart is still owed.

**Response `201 Created`:**
```json
{
  "ID": "container-uuid",
  "Name": "my-vm",
  "Status": "running"
}
```

---

#### `GET /api/containers/{id}`

Get a specific container. Owners and named VM members may read it.

**Response `200 OK`:**
```json
{
  "ID": "container-uuid",
  "Name": "my-vm",
  "Status": "running",
  "CreatedAt": "2024-01-01T00:00:00Z"
}
```

**Errors:**
- `403 Forbidden` — container belongs to another user
- `404 Not Found` — container does not exist

---

#### `DELETE /api/containers/{id}`

Delete a container and all associated data. Requires ownership.

**Response `204 No Content`**

---

#### `POST /api/containers/{id}/start`

Start a stopped container. Requires ownership.

**Response `200 OK`**

---

#### `POST /api/containers/{id}/stop`

Stop a running container. Requires ownership.

**Response `200 OK`**

---

### Shared Links

#### `POST /api/containers/{id}/share`

Create a workload share link. Requires ownership. Optional JSON body:
`{"expires_at":"2027-01-01T00:00:00Z"}`. Omitting expiry makes it non-expiring.
Share links never grant agent or management access.

**Response `201 Created`:**
```json
{
  "token": "share-token",
  "url": "https://my-vm.example.com/?share=share-token",
  "expires_at": null
}
```

---

#### `GET /api/containers/{id}/shares`

List stored shared links for the container. Requires ownership. Responses use
`ID`, `Token`, `ContainerID`, `CreatedBy`, `ExpiresAt`, `CreatedAt`; consumers
should check expiration.

**Response `200 OK`:**
```json
[
  {
    "Token": "share-token",
    "CreatedAt": "2024-01-01T00:00:00Z"
  }
]
```

---

#### `DELETE /api/shares/{token}`

Revoke a shared link by token.

**Response `204 No Content`**

---

### LLM connections

These are the caller's own LLM provider credentials, not platform tokens. Saving
or deleting one re-seeds the models and restarts the agent in every running VM the
caller owns; stopped VMs pick the change up on their next start.

#### `GET /api/keys`

List the caller's connections. The stored key itself is never returned.

**Response `200 OK`:**
```json
[
  {
    "ID": "key-uuid",
    "OwnerID": "user-uuid",
    "Provider": "custom-openmodel",
    "BaseURL": "https://api.openmodel.ai/v1",
    "Models": "deepseek-v4-flash,deepseek-v4-pro",
    "Protocol": "openai-responses",
    "CreatedAt": "2024-01-01T00:00:00Z"
  }
]
```

---

#### `POST /api/keys`

Add or replace a connection. Saving the same `provider` again replaces its settings.

**Request body:**
```json
{
  "provider": "custom-openmodel",
  "base_url": "https://api.openmodel.ai/v1",
  "models": "deepseek-v4-flash,deepseek-v4-pro",
  "protocol": "openai-responses",
  "key": "om-..."
}
```

| Field | Notes |
|---|---|
| `provider` | `openai`, `anthropic`, `gemini`, `fireworks`, `openrouter`, or `custom-<name>` |
| `base_url` | Complete API prefix, no operation path. Required for `custom-*`; defaulted for `openrouter` |
| `models` | Comma-separated model IDs. Required whenever `base_url` is set |
| `protocol` | `openai` (default), `openai-responses`, `anthropic`, `gemini`. Only valid with `base_url` |
| `key` | May be empty for a `custom-*` endpoint that needs no credential |

**Response `201 Created`:** `{"id": "key-uuid"}`

`400 Bad Request` when the provider, URL, model list or protocol is rejected.

---

#### `DELETE /api/keys/{id}`

Delete a connection. If it backed the caller's chosen default model, that choice
is cleared in the same transaction.

**Response `204 No Content`**

---

#### `GET /api/llm/models`

List the models the caller may put their VMs on, most recently configured first,
together with the current choice.

**Response `200 OK`:**
```json
{
  "models": [
    "svkexe_user:custom-openmodel:deepseek-v4-flash",
    "svkexe_user:custom-openmodel:deepseek-v4-pro"
  ],
  "default": ""
}
```

An empty `default` means the gateway picks: the caller's first own model, else
the deployment-wide model.

---

#### `PUT /api/llm/default`

Choose the model every VM on this account opens with — the ones running now and
the ones created later.

**Request body:**
```json
{"model": "svkexe_user:custom-openmodel:deepseek-v4-pro"}
```

Pass `""` to hand the choice back to the gateway.

**Response `200 OK`:** the stored choice, echoed back.

`400 Bad Request` when the model is not one of the caller's own.

---

### SSH Keys

#### `GET /api/ssh-keys`

List all SSH public keys for the authenticated user.

**Response `200 OK`:**
```json
[
  {
    "id": "sshkey-uuid",
    "name": "laptop",
    "fingerprint": "SHA256:...",
    "created_at": "2024-01-01T00:00:00Z"
  }
]
```

---

#### `POST /api/ssh-keys`

Add an SSH public key.

**Request body:**
```json
{
  "name": "laptop",
  "public_key": "ssh-ed25519 AAAA..."
}
```

**Response `201 Created`:**
```json
{
  "id": "sshkey-uuid",
  "name": "laptop",
  "fingerprint": "SHA256:...",
  "created_at": "2024-01-01T00:00:00Z"
}
```

---

#### `DELETE /api/ssh-keys/{id}`

Remove an SSH public key.

**Response `204 No Content`**

---

### Admin

Admin endpoints require the authenticated user to have admin privileges.

#### `GET /api/admin/users`

List all users on the platform.

**Response `200 OK`:**
```json
[
  {
    "id": "user-uuid",
    "email": "user@example.com",
    "created_at": "2024-01-01T00:00:00Z"
  }
]
```

---

#### `DELETE /api/admin/users/{id}`

Delete the user and cascade their gateway database records. This handler does
not delete their Incus instances; remove owned VMs first to avoid orphaning them.

**Response `204 No Content`**

---

#### `GET /api/admin/containers`

List all containers across all users.

**Response `200 OK`:**
```json
[
  {
    "ID": "container-uuid",
    "Name": "my-vm",
    "OwnerID": "user-uuid",
    "Status": "running"
  }
]
```

---

## Additional routes

### Workload, task and rebuild

These routes require VM ownership:

| Method and path | Request / result |
|---|---|
| `POST /api/containers/{id}/recreate` | Start a background rebuild preserving `/data`; returns `202` with an empty body. Poll `GET /api/containers/{id}` |
| `PUT /api/containers/{id}/publish` | `{"port":3000,"public":true}`; returns the container. Port 9000 is reserved |
| `POST /api/containers/{id}/task/retry` | Requeue a failed initial task; returns the current container |

### Custom domains

| Method and path | Request / result |
|---|---|
| `GET /api/containers/{id}/aliases` | List owner's VM aliases |
| `POST /api/containers/{id}/aliases` | `{"hostname":"app.example.net"}`; `201` with alias and DNS verification result |
| `POST /api/containers/{id}/aliases/{aliasID}/verify` | Recheck DNS; `200` with alias |
| `DELETE /api/containers/{id}/aliases/{aliasID}` | Remove alias; `204` |
| `GET /api/tls/check?domain=app.example.net` | Public Caddy certificate authorization; succeeds only for an eligible verified hostname |
| `GET /api/admin/aliases` | Admin list of all aliases |
| `DELETE /api/admin/aliases/{hostname}` | Admin release of a claimed hostname |

A saved alias whose DNS is wrong is not routed; a successful create response
alone does not prove verification. See [workload routing](../README.md#workload-routing).

### Versions and updates (admin only)

| Method and path | Result |
|---|---|
| `GET /api/admin/version` | Gateway and bundled component build versions |
| `GET /api/admin/update/check` | Upstream update check |
| `GET /api/admin/update/status` | Current update progress |
| `POST /api/admin/update` | Request an update through the installed watcher or configured update command |

See [deployment](DEPLOY.md) for installation of the privileged update watcher.

### LLM proxy

`POST /api/llm/v1/chat/completions` accepts OpenAI-style chat requests and
streams responses when `stream: true`. `GET /api/llm/v1/models` lists the
platform fallback models. Routes are registered only when the gateway has an
`OPENROUTER_API_KEY`. Use `Authorization: Bearer <LLM_INTERNAL_TOKEN>` when that token is configured.
Session cookies do not authenticate this proxy. An empty internal token disables
proxy authentication, so set it whenever enabling the platform fallback.
The token is for the VM-to-gateway LLM path, not general administration.

### Metrics

`GET /metrics` returns Prometheus text without authentication; see
[monitoring](DEPLOY.md#monitoring) for the metric names.

## Error Responses

Most management errors are plain text from `http.Error`, for example
`unauthorized` or `container not found`, with the corresponding HTTP status.
Do not assume every error body is JSON. The LLM proxy has a separate error shape.

| Status | Meaning |
|---|---|
| `400 Bad Request` | Invalid request body or parameters |
| `401 Unauthorized` | Missing or invalid authentication |
| `403 Forbidden` | Access to a resource owned by another user |
| `404 Not Found` | Resource does not exist |
| `429 Too Many Requests` | Rate limit exceeded. Check `Retry-After` header. |
| `500 Internal Server Error` | Unexpected server error |
