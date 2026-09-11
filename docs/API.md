# API Reference

## Authentication

All API endpoints require authentication. The gateway validates the `X-ExeDev-Userid` header injected by the Authelia + Caddy auth chain. Direct API access (bypassing the proxy) requires this header to be set.

For API key authentication, include the key in the `Authorization` header:

```
Authorization: Bearer <api-key>
```

API keys can be created via `POST /api/keys`.

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

List all containers owned by the authenticated user.

**Response `200 OK`:**
```json
[
  {
    "id": "container-uuid",
    "name": "my-vm",
    "status": "running",
    "created_at": "2024-01-01T00:00:00Z"
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
  "id": "container-uuid",
  "name": "my-vm",
  "status": "running"
}
```

---

#### `GET /api/containers/{id}`

Get a specific container. Requires ownership.

**Response `200 OK`:**
```json
{
  "id": "container-uuid",
  "name": "my-vm",
  "status": "running",
  "created_at": "2024-01-01T00:00:00Z"
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

Create a shared link for the container. Requires ownership.

**Response `201 Created`:**
```json
{
  "token": "share-token",
  "url": "https://yourdomain.com/shared/share-token"
}
```

---

#### `GET /api/containers/{id}/shares`

List all active shared links for the container. Requires ownership.

**Response `200 OK`:**
```json
[
  {
    "token": "share-token",
    "created_at": "2024-01-01T00:00:00Z"
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
    "public_key": "ssh-ed25519 AAAA...",
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
  "public_key": "ssh-ed25519 AAAA..."
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

Delete a user and all their containers.

**Response `204 No Content`**

---

#### `GET /api/admin/containers`

List all containers across all users.

**Response `200 OK`:**
```json
[
  {
    "id": "container-uuid",
    "name": "my-vm",
    "owner_id": "user-uuid",
    "status": "running"
  }
]
```

---

## Error Responses

All errors return JSON with an `error` field:

```json
{
  "error": "description of what went wrong"
}
```

| Status | Meaning |
|---|---|
| `400 Bad Request` | Invalid request body or parameters |
| `401 Unauthorized` | Missing or invalid authentication |
| `403 Forbidden` | Access to a resource owned by another user |
| `404 Not Found` | Resource does not exist |
| `429 Too Many Requests` | Rate limit exceeded. Check `Retry-After` header. |
| `500 Internal Server Error` | Unexpected server error |
