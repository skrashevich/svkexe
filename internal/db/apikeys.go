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
	ID        string
	OwnerID   string
	Provider  string
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
		`SELECT id, owner_id, provider, created_at FROM api_keys WHERE owner_id = ? ORDER BY created_at DESC`,
		ownerID,
	)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()

	var keys []*APIKey
	for rows.Next() {
		k := &APIKey{}
		if err := rows.Scan(&k.ID, &k.OwnerID, &k.Provider, &k.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// DeleteAPIKey removes an API key by ID.
func (db *DB) DeleteAPIKey(id string) error {
	_, err := db.Exec(`DELETE FROM api_keys WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete api key: %w", err)
	}
	return nil
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
