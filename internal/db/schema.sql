CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    email TEXT UNIQUE NOT NULL,
    display_name TEXT,
    role TEXT NOT NULL DEFAULT 'user',
    password_hash TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS sessions (
    token TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    expires_at DATETIME NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_user_id_idx ON sessions(user_id);
CREATE INDEX IF NOT EXISTS sessions_expires_at_idx ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS containers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    owner_id TEXT NOT NULL REFERENCES users(id),
    incus_name TEXT UNIQUE NOT NULL,
    status TEXT NOT NULL DEFAULT 'creating',
    ip_address TEXT,
    cpu_limit INTEGER DEFAULT 2,
    memory_mb INTEGER DEFAULT 2048,
    disk_gb INTEGER DEFAULT 10,
    app_port INTEGER NOT NULL DEFAULT 3000,
    app_public INTEGER NOT NULL DEFAULT 0,
    initial_task TEXT NOT NULL DEFAULT '',
    initial_task_state TEXT NOT NULL DEFAULT '',
    initial_task_error TEXT NOT NULL DEFAULT '',
    initial_task_conversation TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS containers_owner_name_idx ON containers(owner_id, name);

-- A custom hostname an owner points at their VM. Routing and certificate
-- issuance both go through this table, so an unverified row must never be
-- treated as routable.
CREATE TABLE IF NOT EXISTS container_aliases (
    id TEXT PRIMARY KEY,
    container_id TEXT NOT NULL REFERENCES containers(id) ON DELETE CASCADE,
    hostname TEXT NOT NULL,
    verified INTEGER NOT NULL DEFAULT 0,
    verified_at DATETIME,
    last_error TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Only a VERIFIED alias owns its hostname platform-wide, which is what keeps
-- routing and certificate issuance unambiguous. An unverified claim is not
-- allowed to block anyone: making it exclusive would let a user reserve a
-- domain they do not control and hold it against its real owner forever —
-- precisely what requiring a DNS check is meant to prevent. Two owners may
-- therefore both have the name pending; the first whose DNS actually points
-- here wins, and this index is what settles the race.
CREATE UNIQUE INDEX IF NOT EXISTS container_aliases_verified_hostname_idx
    ON container_aliases(hostname) WHERE verified = 1;

-- One VM lists a hostname once, so a card cannot show the same domain twice.
CREATE UNIQUE INDEX IF NOT EXISTS container_aliases_container_hostname_idx
    ON container_aliases(container_id, hostname);

CREATE INDEX IF NOT EXISTS container_aliases_hostname_lookup_idx ON container_aliases(hostname);
CREATE INDEX IF NOT EXISTS container_aliases_container_idx ON container_aliases(container_id);

CREATE TABLE IF NOT EXISTS api_keys (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES users(id),
    provider TEXT NOT NULL,
    encrypted_key BLOB NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS shared_links (
    id TEXT PRIMARY KEY,
    container_id TEXT NOT NULL REFERENCES containers(id),
    created_by TEXT NOT NULL REFERENCES users(id),
    token TEXT UNIQUE NOT NULL,
    expires_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS ssh_keys (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL REFERENCES users(id),
    fingerprint TEXT UNIQUE NOT NULL,
    public_key TEXT NOT NULL,
    name TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
