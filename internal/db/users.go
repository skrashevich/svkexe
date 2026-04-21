package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// User represents a platform user.
type User struct {
	ID          string
	Email       string
	DisplayName string
	Role        string
	CreatedAt   time.Time
}

// CreateUser inserts a new user into the database.
func (db *DB) CreateUser(u *User) error {
	_, err := db.Exec(
		`INSERT INTO users (id, email, display_name, role) VALUES (?, ?, ?, ?)`,
		u.ID, u.Email, u.DisplayName, u.Role,
	)
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

// GetUserByID returns a user by primary key. Returns sql.ErrNoRows if not found.
func (db *DB) GetUserByID(id string) (*User, error) {
	u := &User{}
	err := db.QueryRow(
		`SELECT id, email, display_name, role, created_at FROM users WHERE id = ?`, id,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	return u, nil
}

// GetUserByEmail returns a user by email. Returns sql.ErrNoRows if not found.
func (db *DB) GetUserByEmail(email string) (*User, error) {
	u := &User{}
	err := db.QueryRow(
		`SELECT id, email, display_name, role, created_at FROM users WHERE email = ?`, email,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("get user by email: %w", err)
	}
	return u, nil
}

// UpdateUser updates display_name and role for the given user.
func (db *DB) UpdateUser(u *User) error {
	_, err := db.Exec(
		`UPDATE users SET display_name = ?, role = ? WHERE id = ?`,
		u.DisplayName, u.Role, u.ID,
	)
	if err != nil {
		return fmt.Errorf("update user: %w", err)
	}
	return nil
}

// ListUsers returns all users ordered by created_at.
func (db *DB) ListUsers() ([]*User, error) {
	rows, err := db.Query(
		`SELECT id, email, display_name, role, created_at FROM users ORDER BY created_at ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		u := &User{}
		if err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role, &u.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// DeleteUser removes a user by ID and cascades to containers and shared_links.
func (db *DB) DeleteUser(id string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("delete user: begin tx: %w", err)
	}
	defer tx.Rollback()

	// Remove shared_links referencing this user's containers (or created by the user).
	if _, err := tx.Exec(
		`DELETE FROM shared_links WHERE created_by = ? OR container_id IN (SELECT id FROM containers WHERE owner_id = ?)`,
		id, id,
	); err != nil {
		return fmt.Errorf("delete user: remove shared_links: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM containers WHERE owner_id = ?`, id); err != nil {
		return fmt.Errorf("delete user: remove containers: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM api_keys WHERE owner_id = ?`, id); err != nil {
		return fmt.Errorf("delete user: remove api_keys: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM users WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return tx.Commit()
}

// EnsureUser returns the existing user with the given id, or creates a new one
// with the provided email if not found (upsert pattern).
func (db *DB) EnsureUser(id, email string) (*User, error) {
	u, err := db.GetUserByID(id)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("ensure user: %w", err)
	}

	newUser := &User{
		ID:    id,
		Email: email,
		Role:  "user",
	}
	if err := db.CreateUser(newUser); err != nil {
		return nil, fmt.Errorf("ensure user: create: %w", err)
	}
	return newUser, nil
}
