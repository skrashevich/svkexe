package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SSHKey represents a user's SSH public key.
type SSHKey struct {
	ID          string
	UserID      string
	Fingerprint string
	PublicKey   string
	Name        string
	CreatedAt   time.Time
}

// CreateSSHKey inserts a new SSH key.
func (db *DB) CreateSSHKey(k *SSHKey) error {
	_, err := db.Exec(
		`INSERT INTO ssh_keys (id, user_id, fingerprint, public_key, name) VALUES (?, ?, ?, ?, ?)`,
		k.ID, k.UserID, k.Fingerprint, k.PublicKey, k.Name,
	)
	if err != nil {
		return fmt.Errorf("create ssh key: %w", err)
	}
	return nil
}

// ListSSHKeysByUser returns all SSH keys for a given user.
func (db *DB) ListSSHKeysByUser(userID string) ([]*SSHKey, error) {
	rows, err := db.Query(
		`SELECT id, user_id, fingerprint, public_key, name, created_at FROM ssh_keys WHERE user_id = ? ORDER BY created_at ASC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list ssh keys: %w", err)
	}
	defer rows.Close()

	var keys []*SSHKey
	for rows.Next() {
		k := &SSHKey{}
		if err := rows.Scan(&k.ID, &k.UserID, &k.Fingerprint, &k.PublicKey, &k.Name, &k.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan ssh key: %w", err)
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// DeleteSSHKey removes an SSH key by ID, only if it belongs to ownerID.
func (db *DB) DeleteSSHKey(id, ownerID string) error {
	res, err := db.Exec(`DELETE FROM ssh_keys WHERE id = ? AND user_id = ?`, id, ownerID)
	if err != nil {
		return fmt.Errorf("delete ssh key: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GetUserBySSHFingerprint returns the user whose SSH key has the given fingerprint.
func (db *DB) GetUserBySSHFingerprint(fingerprint string) (*User, error) {
	u := &User{}
	err := db.QueryRow(
		`SELECT u.id, u.email, u.display_name, u.role, u.created_at
		 FROM users u
		 JOIN ssh_keys k ON k.user_id = u.id
		 WHERE k.fingerprint = ?`,
		fingerprint,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("get user by ssh fingerprint: %w", err)
	}
	return u, nil
}
