package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// SharedLink represents a shareable access link for a container.
type SharedLink struct {
	ID          string
	ContainerID string
	CreatedBy   string
	Token       string
	ExpiresAt   *time.Time
	CreatedAt   time.Time
}

// CreateSharedLink generates a new shared link for the given container.
func (db *DB) CreateSharedLink(containerID, createdBy string, expiresAt *time.Time) (*SharedLink, error) {
	rawToken := make([]byte, 32)
	if _, err := rand.Read(rawToken); err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(rawToken)

	var expiresAtUTC *time.Time
	if expiresAt != nil {
		t := expiresAt.UTC()
		expiresAtUTC = &t
	}

	link := &SharedLink{
		ID:          uuid.New().String(),
		ContainerID: containerID,
		CreatedBy:   createdBy,
		Token:       token,
		ExpiresAt:   expiresAtUTC,
	}

	_, err := db.Exec(
		`INSERT INTO shared_links (id, container_id, created_by, token, expires_at) VALUES (?, ?, ?, ?, ?)`,
		link.ID, link.ContainerID, link.CreatedBy, link.Token, link.ExpiresAt,
	)
	if err != nil {
		return nil, fmt.Errorf("create shared link: %w", err)
	}

	// Fetch back to populate CreatedAt.
	return db.getSharedLinkByID(link.ID)
}

// GetSharedLinkByToken returns a shared link by token, checking expiration.
func (db *DB) GetSharedLinkByToken(token string) (*SharedLink, error) {
	link := &SharedLink{}
	var expiresAt sql.NullTime
	err := db.QueryRow(
		`SELECT id, container_id, created_by, token, expires_at, created_at
		 FROM shared_links
		 WHERE token = ? AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)`,
		token,
	).Scan(&link.ID, &link.ContainerID, &link.CreatedBy, &link.Token, &expiresAt, &link.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("get shared link by token: %w", err)
	}
	if expiresAt.Valid {
		link.ExpiresAt = &expiresAt.Time
	}
	return link, nil
}

// ListSharedLinksByContainer returns all shared links for a container.
func (db *DB) ListSharedLinksByContainer(containerID string) ([]*SharedLink, error) {
	rows, err := db.Query(
		`SELECT id, container_id, created_by, token, expires_at, created_at
		 FROM shared_links WHERE container_id = ? ORDER BY created_at DESC`,
		containerID,
	)
	if err != nil {
		return nil, fmt.Errorf("list shared links: %w", err)
	}
	defer rows.Close()

	var links []*SharedLink
	for rows.Next() {
		link := &SharedLink{}
		var expiresAt sql.NullTime
		if err := rows.Scan(&link.ID, &link.ContainerID, &link.CreatedBy, &link.Token, &expiresAt, &link.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan shared link: %w", err)
		}
		if expiresAt.Valid {
			link.ExpiresAt = &expiresAt.Time
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

// DeleteSharedLink removes a shared link by ID.
func (db *DB) DeleteSharedLink(id string) error {
	_, err := db.Exec(`DELETE FROM shared_links WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete shared link: %w", err)
	}
	return nil
}

// DeleteSharedLinkByToken removes a shared link by token.
func (db *DB) DeleteSharedLinkByToken(token string) error {
	_, err := db.Exec(`DELETE FROM shared_links WHERE token = ?`, token)
	if err != nil {
		return fmt.Errorf("delete shared link by token: %w", err)
	}
	return nil
}

// getSharedLinkByID is an internal helper to fetch a link by primary key.
func (db *DB) getSharedLinkByID(id string) (*SharedLink, error) {
	link := &SharedLink{}
	var expiresAt sql.NullTime
	err := db.QueryRow(
		`SELECT id, container_id, created_by, token, expires_at, created_at
		 FROM shared_links WHERE id = ?`, id,
	).Scan(&link.ID, &link.ContainerID, &link.CreatedBy, &link.Token, &expiresAt, &link.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("get shared link by id: %w", err)
	}
	if expiresAt.Valid {
		link.ExpiresAt = &expiresAt.Time
	}
	return link, nil
}
