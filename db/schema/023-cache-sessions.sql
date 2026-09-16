-- Browser cache sessions used to derive the client-side IndexedDB
-- encryption key. Each row represents a long-lived cookie issued to a
-- browser. The raw cookie value is NEVER stored; only its SHA-256 hash
-- (token_hash). The server-side master secret used as HKDF IKM lives in
-- the settings table under key 'cache_session_master_secret'.
--
-- A row is opaque to other features: deleting one effectively logs that
-- browser out of the IDB cache (next /api/cache-key issues a fresh
-- cookie / new key_id, and the client wipes its IDB).
CREATE TABLE IF NOT EXISTS cache_sessions (
    token_hash    TEXT PRIMARY KEY,
    user_id       TEXT NOT NULL,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_seen_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_cache_sessions_last_seen ON cache_sessions(last_seen_at);
