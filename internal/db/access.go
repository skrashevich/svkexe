package db

import (
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

const GuestRole = "guest"

var ErrAccessDenied = errors.New("access denied")

// CanUseContainer grants use, never lifecycle management or sharing.
func (db *DB) CanUseContainer(id, userID string) bool {
	var allowed bool
	err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM containers c JOIN users u ON u.id = ? WHERE c.id = ? AND (c.owner_id = u.id OR EXISTS(SELECT 1 FROM container_access a WHERE a.container_id = c.id AND a.user_id = u.id)))`, userID, id).Scan(&allowed)
	return err == nil && allowed
}

func (db *DB) ListAccessibleContainers(userID string) ([]*Container, error) {
	rows, err := db.Query(`SELECT `+containerColumns+` FROM containers WHERE owner_id = ? OR id IN (SELECT container_id FROM container_access WHERE user_id = ?) ORDER BY created_at DESC`, userID, userID)
	if err != nil {
		return nil, fmt.Errorf("list accessible VMs: %w", err)
	}
	defer rows.Close()
	containers := []*Container{}
	for rows.Next() {
		c, err := scanContainer(rows)
		if err != nil {
			return nil, err
		}
		c.SharedAccess = c.OwnerID != userID
		containers = append(containers, c)
	}
	return containers, rows.Err()
}

// ResolveAccessibleContainer accepts an exact ID/Incus name, or an unambiguous
// display name. It never chooses arbitrarily between two owners' same-name VMs.
func (db *DB) ResolveAccessibleContainer(ref, userID string) (*Container, error) {
	containers, err := db.ListAccessibleContainers(userID)
	if err != nil {
		return nil, err
	}
	for _, c := range containers {
		if c.ID == ref || c.IncusName == ref {
			return c, nil
		}
	}
	return uniqueAccessibleName(containers, ref)
}

// GetAccessibleContainerByName resolves only display names for HTTP hosts.
// IDs and Incus names must not shadow another VM's published hostname.
func (db *DB) GetAccessibleContainerByName(name, userID string) (*Container, error) {
	containers, err := db.ListAccessibleContainers(userID)
	if err != nil {
		return nil, err
	}
	return uniqueAccessibleName(containers, name)
}

func uniqueAccessibleName(containers []*Container, ref string) (*Container, error) {
	var found *Container
	for _, c := range containers {
		if c.Name == ref {
			if found != nil {
				return nil, fmt.Errorf("ambiguous VM name; use the VM ID or Incus name")
			}
			found = c
		}
	}
	if found == nil {
		return nil, sql.ErrNoRows
	}
	return found, nil
}

// GrantContainerAccess atomically validates ownership and binds a verified key
// to a new guest. Existing identities must present a key already on that account:
// an inviter must never be able to add their own key to someone else's account.
func (db *DB) GrantContainerAccess(ownerID, containerID, email, publicKey string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > 254 {
		return fmt.Errorf("enter a valid email address")
	}
	key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(publicKey)))
	if err != nil || len(rest) != 0 || len(options) != 0 {
		return fmt.Errorf("enter one SSH public key without authorized_keys options")
	}
	if _, cert := key.(*ssh.Certificate); cert {
		return fmt.Errorf("SSH certificates are not supported")
	}
	fingerprint := ssh.FingerprintSHA256(key)
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var permitted bool
	if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM containers c JOIN users u ON u.id=c.owner_id WHERE c.id=? AND c.owner_id=? AND u.role != ?)`, containerID, ownerID, GuestRole).Scan(&permitted); err != nil {
		return err
	}
	if !permitted {
		return ErrAccessDenied
	}
	var userID string
	err = tx.QueryRow(`SELECT id FROM users WHERE lower(email)=lower(?)`, email).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		userID = uuid.NewString()
		if _, err = tx.Exec(`INSERT INTO users(id,email,display_name,role) VALUES(?,?,?,?)`, userID, email, email, GuestRole); err != nil {
			return fmt.Errorf("create guest: %w", err)
		}
		if _, err = tx.Exec(`INSERT INTO ssh_keys(id,user_id,fingerprint,public_key,name) VALUES(?,?,?,?,?)`, uuid.NewString(), userID, fingerprint, string(ssh.MarshalAuthorizedKey(key)), "Invitation"); err != nil {
			return fmt.Errorf("SSH key is already registered or could not be saved")
		}
	} else if err != nil {
		return err
	} else {
		var matches bool
		if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM ssh_keys WHERE user_id=? AND fingerprint=?)`, userID, fingerprint).Scan(&matches); err != nil {
			return err
		}
		if !matches {
			return fmt.Errorf("existing account requires one of its already registered SSH keys")
		}
	}
	if userID == ownerID {
		return fmt.Errorf("the owner already has access")
	}
	_, err = tx.Exec(`INSERT INTO container_access(container_id,user_id) VALUES(?,?) ON CONFLICT DO NOTHING`, containerID, userID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) ListContainerAccess(containerID string) ([]*User, error) {
	rows, err := db.Query(`SELECT u.id,u.email FROM users u JOIN container_access a ON a.user_id=u.id WHERE a.container_id=? ORDER BY u.email`, containerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []*User{}
	for rows.Next() {
		u := &User{}
		if err := rows.Scan(&u.ID, &u.Email); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (db *DB) RevokeContainerAccess(ownerID, containerID, userID string) error {
	var permitted bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM containers c JOIN users u ON u.id=c.owner_id WHERE c.id=? AND c.owner_id=? AND u.role != ?)`, containerID, ownerID, GuestRole).Scan(&permitted); err != nil {
		return err
	}
	if !permitted {
		return ErrAccessDenied
	}
	_, err := db.Exec(`DELETE FROM container_access WHERE container_id=? AND user_id=?`, containerID, userID)
	return err
}
