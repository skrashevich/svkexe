package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Container represents a managed Incus container.
type Container struct {
	ID        string
	Name      string
	OwnerID   string
	IncusName string
	Status    string
	IPAddress string
	CPULimit  int
	MemoryMB  int
	DiskGB    int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateContainer inserts a new container record.
func (db *DB) CreateContainer(c *Container) error {
	_, err := db.Exec(
		`INSERT INTO containers (id, name, owner_id, incus_name, status, ip_address, cpu_limit, memory_mb, disk_gb)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.OwnerID, c.IncusName, c.Status, c.IPAddress,
		c.CPULimit, c.MemoryMB, c.DiskGB,
	)
	if err != nil {
		return fmt.Errorf("create container: %w", err)
	}
	return nil
}

// GetContainerByID returns a container by primary key.
func (db *DB) GetContainerByID(id string) (*Container, error) {
	c := &Container{}
	err := db.QueryRow(
		`SELECT id, name, owner_id, incus_name, status, COALESCE(ip_address,''), cpu_limit, memory_mb, disk_gb, created_at, updated_at
		 FROM containers WHERE id = ?`, id,
	).Scan(&c.ID, &c.Name, &c.OwnerID, &c.IncusName, &c.Status, &c.IPAddress,
		&c.CPULimit, &c.MemoryMB, &c.DiskGB, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("get container by id: %w", err)
	}
	return c, nil
}

// ListContainersByOwner returns all containers belonging to ownerID.
func (db *DB) ListContainersByOwner(ownerID string) ([]*Container, error) {
	rows, err := db.Query(
		`SELECT id, name, owner_id, incus_name, status, COALESCE(ip_address,''), cpu_limit, memory_mb, disk_gb, created_at, updated_at
		 FROM containers WHERE owner_id = ? ORDER BY created_at DESC`, ownerID,
	)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	defer rows.Close()

	var containers []*Container
	for rows.Next() {
		c := &Container{}
		if err := rows.Scan(&c.ID, &c.Name, &c.OwnerID, &c.IncusName, &c.Status, &c.IPAddress,
			&c.CPULimit, &c.MemoryMB, &c.DiskGB, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan container: %w", err)
		}
		containers = append(containers, c)
	}
	return containers, rows.Err()
}

// GetContainerByName returns a container by name and ownerID.
func (db *DB) GetContainerByName(name, ownerID string) (*Container, error) {
	c := &Container{}
	err := db.QueryRow(
		`SELECT id, name, owner_id, incus_name, status, COALESCE(ip_address,''), cpu_limit, memory_mb, disk_gb, created_at, updated_at
		 FROM containers WHERE name = ? AND owner_id = ?`, name, ownerID,
	).Scan(&c.ID, &c.Name, &c.OwnerID, &c.IncusName, &c.Status, &c.IPAddress,
		&c.CPULimit, &c.MemoryMB, &c.DiskGB, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("get container by name: %w", err)
	}
	return c, nil
}

// GetContainerByNameOnly returns a container by name (without owner filter).
// Used when ownership is checked separately.
func (db *DB) GetContainerByNameOnly(name string) (*Container, error) {
	c := &Container{}
	err := db.QueryRow(
		`SELECT id, name, owner_id, incus_name, status, COALESCE(ip_address,''), cpu_limit, memory_mb, disk_gb, created_at, updated_at
		 FROM containers WHERE name = ?`, name,
	).Scan(&c.ID, &c.Name, &c.OwnerID, &c.IncusName, &c.Status, &c.IPAddress,
		&c.CPULimit, &c.MemoryMB, &c.DiskGB, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("get container by name: %w", err)
	}
	return c, nil
}

// UpdateContainerStatus updates status and ip_address, refreshing updated_at.
func (db *DB) UpdateContainerStatus(id, status, ipAddress string) error {
	_, err := db.Exec(
		`UPDATE containers SET status = ?, ip_address = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		status, ipAddress, id,
	)
	if err != nil {
		return fmt.Errorf("update container status: %w", err)
	}
	return nil
}

// ListAllContainers returns all containers across all owners ordered by created_at DESC.
func (db *DB) ListAllContainers() ([]*Container, error) {
	rows, err := db.Query(
		`SELECT id, name, owner_id, incus_name, status, COALESCE(ip_address,''), cpu_limit, memory_mb, disk_gb, created_at, updated_at
		 FROM containers ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list all containers: %w", err)
	}
	defer rows.Close()

	var containers []*Container
	for rows.Next() {
		c := &Container{}
		if err := rows.Scan(&c.ID, &c.Name, &c.OwnerID, &c.IncusName, &c.Status, &c.IPAddress,
			&c.CPULimit, &c.MemoryMB, &c.DiskGB, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan container: %w", err)
		}
		containers = append(containers, c)
	}
	return containers, rows.Err()
}

// DeleteContainer removes a container record by ID.
func (db *DB) DeleteContainer(id string) error {
	_, err := db.Exec(`DELETE FROM containers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete container: %w", err)
	}
	return nil
}
