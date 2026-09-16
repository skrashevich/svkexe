-- name: GetAllFeatureFlags :many
SELECT name, value FROM feature_flags;

-- name: SetFeatureFlag :exec
INSERT INTO feature_flags (name, value, updated_at)
VALUES (?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(name) DO UPDATE SET
    value = excluded.value,
    updated_at = CURRENT_TIMESTAMP;

-- name: DeleteFeatureFlag :exec
DELETE FROM feature_flags WHERE name = ?;
