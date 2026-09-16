-- name: GetCacheSession :one
SELECT token_hash, user_id, created_at, last_seen_at
FROM cache_sessions
WHERE token_hash = ?;

-- name: UpsertCacheSession :exec
INSERT INTO cache_sessions (token_hash, user_id, created_at, last_seen_at)
VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT(token_hash) DO UPDATE SET
    user_id      = excluded.user_id,
    last_seen_at = CURRENT_TIMESTAMP;

-- name: TouchCacheSession :exec
UPDATE cache_sessions
SET last_seen_at = CURRENT_TIMESTAMP
WHERE token_hash = ?;

-- name: DeleteCacheSession :exec
DELETE FROM cache_sessions WHERE token_hash = ?;
