-- Feature flag overrides. The set of recognized flags lives in code
-- (package featureflags); rows whose name is not registered are ignored.
-- Deleting a row reverts to the code-defined default.
CREATE TABLE IF NOT EXISTS feature_flags (
    name TEXT PRIMARY KEY,
    value TEXT NOT NULL, -- JSON-encoded value
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
