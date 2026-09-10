package db

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"
)

// APIKey represents an encrypted LLM provider API key.
type APIKey struct {
	ID       string
	OwnerID  string
	Provider string
	BaseURL  string
	Models   string
	// Protocol is the wire protocol the endpoint speaks. It is empty for a
	// provider-native key, which has no endpoint to speak one to.
	Protocol  string
	CreatedAt time.Time
}

// CreateAPIKey encrypts plaintext using AES-GCM with encKey and stores it.
// encKey must be 16, 24, or 32 bytes.
func (db *DB) CreateAPIKey(id, ownerID, provider, plaintext string, encKey []byte) error {
	encrypted, err := encryptAESGCM(encKey, []byte(plaintext))
	if err != nil {
		return fmt.Errorf("encrypt api key: %w", err)
	}
	_, err = db.Exec(
		`INSERT INTO api_keys (id, owner_id, provider, encrypted_key) VALUES (?, ?, ?, ?)`,
		id, ownerID, provider, encrypted,
	)
	if err != nil {
		return fmt.Errorf("create api key: %w", err)
	}
	return nil
}

// GetAPIKeyPlaintext retrieves and decrypts an API key by ID.
func (db *DB) GetAPIKeyPlaintext(id string, encKey []byte) (string, error) {
	var encrypted []byte
	err := db.QueryRow(`SELECT encrypted_key FROM api_keys WHERE id = ?`, id).Scan(&encrypted)
	if errors.Is(err, sql.ErrNoRows) {
		return "", sql.ErrNoRows
	}
	if err != nil {
		return "", fmt.Errorf("get api key: %w", err)
	}
	plain, err := decryptAESGCM(encKey, encrypted)
	if err != nil {
		return "", fmt.Errorf("decrypt api key: %w", err)
	}
	return string(plain), nil
}

// ListAPIKeysByOwner returns metadata (no plaintext) for all keys owned by ownerID.
func (db *DB) ListAPIKeysByOwner(ownerID string) ([]*APIKey, error) {
	rows, err := db.Query(
		`SELECT id, owner_id, provider, base_url, models, protocol, created_at FROM api_keys WHERE owner_id = ? ORDER BY created_at DESC`,
		ownerID,
	)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()

	var keys []*APIKey
	for rows.Next() {
		k := &APIKey{}
		if err := rows.Scan(&k.ID, &k.OwnerID, &k.Provider, &k.BaseURL, &k.Models, &k.Protocol, &k.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// DeleteAPIKey removes an API key by ID. A key that is already gone is not an
// error: this entry point is used to clear out keys the caller has just listed,
// where a concurrent delete is a race it does not need to hear about.
func (db *DB) DeleteAPIKey(id string) error {
	if err := db.deleteAPIKey(id, ""); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

// DeleteAPIKeyForOwner removes an API key only when it belongs to ownerID.
// Returns sql.ErrNoRows when no matching row was deleted, which is how a
// request for someone else's key is answered — indistinguishably from one for a
// key that does not exist.
func (db *DB) DeleteAPIKeyForOwner(id, ownerID string) error {
	return db.deleteAPIKey(id, ownerID)
}

// deleteAPIKey removes a connection and, in the same transaction, drops a
// chosen default model the deletion just made unreachable. The two have to move
// together: between them the owner's VMs would be pointed at a model no longer
// in their agent database, and a caller that crashed in between would leave
// them there permanently.
//
// ownerID, when non-empty, scopes the delete to that owner.
//
// The delete comes first and reports the owner through RETURNING rather than
// being preceded by a SELECT. That is deliberate on two counts: it removes the
// window in which the row could change between the two statements, and it makes
// the transaction's first statement a write. A transaction that reads first
// holds a WAL read snapshot and has to upgrade to a write lock, and SQLite
// refuses that upgrade with SQLITE_BUSY_SNAPSHOT the moment anyone else has
// committed in between — a busy that busy_timeout does not retry, which would
// surface as a spurious failure on a perfectly valid delete.
func (db *DB) deleteAPIKey(id, ownerID string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("delete api key: begin tx: %w", err)
	}
	defer tx.Rollback()
	// The scoping is a whole separate statement rather than a predicate that
	// switches itself off, so that an empty ownerID cannot quietly turn an
	// owner-scoped delete into a delete of anybody's key — and a prune of that
	// owner's chosen model.
	query, args := `DELETE FROM api_keys WHERE id = ? RETURNING owner_id`, []any{id}
	if ownerID != "" {
		query, args = `DELETE FROM api_keys WHERE id = ? AND owner_id = ? RETURNING owner_id`, []any{id, ownerID}
	}
	var owner string
	err = tx.QueryRow(query, args...).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		// No such key, or it belongs to someone else. The two are deliberately
		// indistinguishable to the caller.
		return sql.ErrNoRows
	}
	if err != nil {
		return fmt.Errorf("delete api key: %w", err)
	}
	if err := pruneDefaultModel(tx, owner); err != nil {
		return err
	}
	return tx.Commit()
}

// encryptAESGCM encrypts plaintext using AES-GCM. Returns nonce+ciphertext.
func encryptAESGCM(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// decryptAESGCM decrypts data produced by encryptAESGCM.
func decryptAESGCM(key, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(data) < ns {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := data[:ns], data[ns:]
	return gcm.Open(nil, nonce, ciphertext, nil)
}
