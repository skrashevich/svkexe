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

Create a new container.

**Request body:**
```json
{
  "name": "my-vm"
}
```

**Response `201 Created`:**
```json
{
  "id": "container-uuid",
  "name": "my-vm",
  "status": "creating"
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

### API Keys

#### `GET /api/keys`

List all API keys for the authenticated user.

**Response `200 OK`:**
```json
[
  {
    "id": "key-uuid",
    "name": "my-key",
    "created_at": "2024-01-01T00:00:00Z",
    "last_used_at": "2024-01-02T00:00:00Z"
  }
]
```

---

#### `POST /api/keys`

Create a new API key.

**Request body:**
```json
{
  "name": "my-key"
}
```

**Response `201 Created`:**
```json
{
  "id": "key-uuid",
  "name": "my-key",
  "key": "svk_..."
}
```

Note: the `key` value is only returned once at creation time.

---

#### `DELETE /api/keys/{id}`

Delete an API key.

**Response `204 No Content`**

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
