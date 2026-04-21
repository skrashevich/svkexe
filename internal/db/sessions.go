package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Session is an authenticated user session backed by a cookie token.
type Session struct {
	Token     string
	UserID    string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// SessionTTL is the default lifetime for newly created sessions.
const SessionTTL = 7 * 24 * time.Hour

// NewSessionToken returns a cryptographically random 64-hex-char token.
func NewSessionToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("session token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// CreateSession inserts a new session for userID valid for SessionTTL.
func (db *DB) CreateSession(userID string) (*Session, error) {
	token, err := NewSessionToken()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	exp := now.Add(SessionTTL)
	_, err = db.Exec(
		`INSERT INTO sessions (token, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		token, userID, now, exp,
	)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return &Session{Token: token, UserID: userID, CreatedAt: now, ExpiresAt: exp}, nil
}

// GetSession returns the session with the given token if it exists and has not
// expired. Returns sql.ErrNoRows for missing/expired sessions.
func (db *DB) GetSession(token string) (*Session, error) {
	s := &Session{}
	err := db.QueryRow(
		`SELECT token, user_id, created_at, expires_at FROM sessions WHERE token = ? AND expires_at > ?`,
		token, time.Now().UTC(),
	).Scan(&s.Token, &s.UserID, &s.CreatedAt, &s.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	return s, nil
}

// DeleteSession removes a single session by token (idempotent).
func (db *DB) DeleteSession(token string) error {
	if _, err := db.Exec(`DELETE FROM sessions WHERE token = ?`, token); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteExpiredSessions purges sessions past their expires_at.
func (db *DB) DeleteExpiredSessions() (int64, error) {
	res, err := db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, time.Now().UTC())
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
