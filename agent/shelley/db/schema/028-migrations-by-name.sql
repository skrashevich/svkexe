-- Track applied migrations by filename rather than number.
--
-- Background: the original migrations table used migration_number as the
-- PRIMARY KEY. Two migration files with the same numeric prefix (e.g. two
-- feature branches both numbering their migration 017) couldn't coexist,
-- and worse, if one of them already ran on a database and was later
-- renamed/renumbered upstream, the runner silently skipped the new one
-- because a row with that number already existed.
--
-- Rebuild the table so:
--   - migration_name is the natural key (UNIQUE)
--   - migration_number stays for ordering/inspection but is not unique
--
-- No PRAGMA foreign_keys dance is needed because nothing in the schema
-- references the migrations table.
CREATE TABLE migrations_new (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    migration_number INTEGER NOT NULL,
    migration_name TEXT NOT NULL UNIQUE,
    executed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO migrations_new (migration_number, migration_name, executed_at)
SELECT migration_number, migration_name, executed_at FROM migrations;

DROP TABLE migrations;
ALTER TABLE migrations_new RENAME TO migrations;
