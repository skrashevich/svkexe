package db

import (
	"database/sql"
	"errors"
	"fmt"
)

// SettingNestingAllowed is the deployment-wide ceiling on nested containers.
// It is a ceiling and not a default: a VM may only enable nesting while this is
// on, which is what lets an operator take the capability away from every tenant
// at once without editing rows one by one.
const SettingNestingAllowed = "nesting_allowed"

// GetSetting returns a stored operator setting, or def when it was never set.
func (db *DB) GetSetting(key, def string) (string, error) {
	var value string
	err := db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return def, nil
	}
	if err != nil {
		return "", fmt.Errorf("get setting %q: %w", key, err)
	}
	return value, nil
}

// SetSetting stores an operator setting, replacing any previous value.
func (db *DB) SetSetting(key, value string) error {
	_, err := db.Exec(
		`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = CURRENT_TIMESTAMP`,
		key, value,
	)
	if err != nil {
		return fmt.Errorf("set setting %q: %w", key, err)
	}
	return nil
}

// NestingAllowed reports whether VMs may run nested containers at all. A
// deployment that has never touched the switch allows it: the platform exists
// to run other people's builds, and refusing Docker by default is the surprise,
// not the safe choice.
func (db *DB) NestingAllowed() (bool, error) {
	v, err := db.GetSetting(SettingNestingAllowed, "1")
	if err != nil {
		return false, err
	}
	return v == "1", nil
}

// SetNestingAllowed stores the deployment-wide ceiling.
func (db *DB) SetNestingAllowed(allowed bool) error {
	value := "0"
	if allowed {
		value = "1"
	}
	return db.SetSetting(SettingNestingAllowed, value)
}
