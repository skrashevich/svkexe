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

// migrate runs the embedded schema SQL to create tables if they don't exist.
func (db *DB) migrate() error {
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("exec schema: %w", err)
	}
	return nil
}
