package db

import (
	"database/sql"
	_ "embed"
	"fmt"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

// DB wraps sql.DB with application-specific helpers.
type DB struct {
	*sql.DB
}

// Open opens (or creates) a SQLite database at the given path.
// It enables WAL mode and sets busy_timeout to 5000 ms, then runs migrations.
func Open(path string) (*DB, error) {
	sqldb, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	if _, err = sqldb.Exec("PRAGMA journal_mode=WAL"); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	if _, err = sqldb.Exec("PRAGMA busy_timeout=5000"); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

	if _, err = sqldb.Exec("PRAGMA foreign_keys=ON"); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	db := &DB{sqldb}
	if err = db.migrate(); err != nil {
		sqldb.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return db, nil
}

// migrate runs the embedded schema SQL to create tables if they don't exist,
// then applies idempotent ALTER statements for columns added to existing tables.
func (db *DB) migrate() error {
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("exec schema: %w", err)
	}

	// Pre-existing users tables need these backfilled. SQLite lacks
	// IF NOT EXISTS for ADD COLUMN, so probe table_info first.
	for column, definition := range map[string]string{
		"password_hash": "TEXT NOT NULL DEFAULT ''",
		// An upgraded account has made no choice yet, so the empty value keeps
		// its VMs on whichever model the gateway was already picking for them.
		"default_model": "TEXT NOT NULL DEFAULT ''",
	} {
		exists, err := columnExists(db, "users", column)
		if err != nil {
			return fmt.Errorf("probe users.%s: %w", column, err)
		}
		if !exists {
			if _, err := db.Exec("ALTER TABLE users ADD COLUMN " + column + " " + definition); err != nil {
				return fmt.Errorf("add users.%s column: %w", column, err)
			}
		}
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS containers_owner_name_idx ON containers(owner_id, name)`); err != nil {
		return fmt.Errorf("create containers_owner_name_idx: %w", err)
	}
	// Custom domains were briefly unique across every row rather than only
	// across verified ones, which let an unverified claim hold a name against
	// its real owner. Dropping the old index is what lets those pending claims
	// coexist until one of them actually verifies.
	if _, err := db.Exec(`DROP INDEX IF EXISTS container_aliases_hostname_idx`); err != nil {
		return fmt.Errorf("drop container_aliases_hostname_idx: %w", err)
	}
	// Existing VMs must stay private after the upgrade, so both columns default
	// to the safe value rather than to whatever the workload happens to serve.
	for column, definition := range map[string]string{
		"app_port":   "INTEGER NOT NULL DEFAULT 3000",
		"app_public": "INTEGER NOT NULL DEFAULT 0",
		// An upgraded VM has no queued task, so the empty state keeps delivery
		// from firing on its next start.
		"initial_task":       "TEXT NOT NULL DEFAULT ''",
		"initial_task_state": "TEXT NOT NULL DEFAULT ''",
		"initial_task_error": "TEXT NOT NULL DEFAULT ''",
		// Names the agent conversation the task runs in, so progress can be
		// polled after delivery.
		"initial_task_conversation": "TEXT NOT NULL DEFAULT ''",
		// Counts the times the gateway has picked this task back up after a
		// transient agent failure, which is what bounds it.
		"initial_task_resumes": "INTEGER NOT NULL DEFAULT 0",
		// Nested containers are on by default, so an upgraded VM inherits the
		// same answer a new one gets rather than staying silently unable to run
		// Docker. The applied column deliberately backfills to 0: those VMs were
		// built under a profile that disabled nesting, so until they restart the
		// setting is a wish, and saying otherwise would have the dashboard claim
		// Docker works when it still does not.
		"nesting":         "INTEGER NOT NULL DEFAULT 1",
		"nesting_applied": "INTEGER NOT NULL DEFAULT 0",
	} {
		exists, err := columnExists(db, "containers", column)
		if err != nil {
			return err
		}
		if !exists {
			if _, err := db.Exec("ALTER TABLE containers ADD COLUMN " + column + " " + definition); err != nil {
				return err
			}
		}
	}
	for _, column := range []string{"base_url", "models", "protocol"} {
		exists, err := columnExists(db, "api_keys", column)
		if err != nil {
			return err
		}
		if !exists {
			if _, err := db.Exec("ALTER TABLE api_keys ADD COLUMN " + column + " TEXT NOT NULL DEFAULT ''"); err != nil {
				return err
			}
		}
	}
	// A connection stored before the protocol was configurable was seeded as
	// chat/completions. Naming that explicitly leaves the empty value meaning
	// exactly one thing — a provider-native key, which has no endpoint to speak
	// a protocol to — rather than two, so nothing downstream has to re-derive
	// which of the two it is looking at.
	if _, err := db.Exec(
		`UPDATE api_keys SET protocol = ? WHERE protocol = '' AND base_url != ''`, DefaultProtocol,
	); err != nil {
		return fmt.Errorf("backfill api_keys.protocol: %w", err)
	}
	return nil
}

func columnExists(db *DB, table, column string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
