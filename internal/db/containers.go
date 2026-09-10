package db

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	// DefaultAppPort is where the bare VM host sends traffic until the owner
	// picks another port.
	DefaultAppPort = 3000

	// AgentPort mirrors picoclaw.Port — redeclared here to avoid importing the
	// picoclaw package, which already depends on this one.
	AgentPort = 9000
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
	// AppPort is the in-VM port the bare VM host proxies to.
	AppPort int
	// AppPublic serves the workload without a session. It never applies to the
	// agent or to explicit-port hosts.
	AppPublic bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// containerColumns keeps every read of a container in sync.
const containerColumns = `id, name, owner_id, incus_name, status, COALESCE(ip_address,''), cpu_limit, memory_mb, disk_gb, app_port, app_public, created_at, updated_at`

func scanContainer(row interface{ Scan(...any) error }) (*Container, error) {
	c := &Container{}
	err := row.Scan(&c.ID, &c.Name, &c.OwnerID, &c.IncusName, &c.Status, &c.IPAddress,
		&c.CPULimit, &c.MemoryMB, &c.DiskGB, &c.AppPort, &c.AppPublic, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// containerNameRE restricts names to what can appear in a DNS label.
var containerNameRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// explicitPortRE matches the "{port}-{name}" workload host prefix.
var explicitPortRE = regexp.MustCompile(`^[0-9]{1,5}-`)

// AgentHostPrefix gives the agent its own single-label host, so one wildcard
// certificate covers it. A nested "agent.<vm>.<domain>" would not be covered.
const AgentHostPrefix = "agent-"

// ReservedName reports whether a VM name would collide with a routing prefix.
// A VM named "agent-foo" would otherwise shadow the agent host of VM "foo".
func ReservedName(name string) bool {
	return strings.HasPrefix(name, AgentHostPrefix) || explicitPortRE.MatchString(name)
}

// ValidContainerName reports whether a name is usable as a subdomain label and
// does not collide with a routing prefix.
func ValidContainerName(name string) bool {
	return len(name) >= 2 && len(name) <= 63 && containerNameRE.MatchString(name) && !ReservedName(name)
}

// ValidAppPort rejects ports that cannot be published. The agent port is
// excluded because publishing it would expose command execution and the whole
// container filesystem to anonymous callers.
func ValidAppPort(port int) bool {
	return port > 0 && port <= 65535 && port != AgentPort
}

// UpdateContainerPublish stores the workload port and its visibility.
func (db *DB) UpdateContainerPublish(id string, port int, public bool) error {
	if !ValidAppPort(port) {
		return fmt.Errorf("port must be between 1 and 65535 and must not be %d", AgentPort)
	}
	_, err := db.Exec(
		`UPDATE containers SET app_port = ?, app_public = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		port, public, id,
	)
	if err != nil {
		return fmt.Errorf("update container publish settings: %w", err)
	}
	return nil
}

// CreateContainer inserts a new container record.
func (db *DB) CreateContainer(c *Container) error {
	if c.AppPort == 0 {
		c.AppPort = DefaultAppPort
	}
	if !ValidAppPort(c.AppPort) {
		return fmt.Errorf("invalid app port %d", c.AppPort)
	}
	_, err := db.Exec(
		`INSERT INTO containers (id, name, owner_id, incus_name, status, ip_address, cpu_limit, memory_mb, disk_gb, app_port, app_public)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.OwnerID, c.IncusName, c.Status, c.IPAddress,
		c.CPULimit, c.MemoryMB, c.DiskGB, c.AppPort, c.AppPublic,
	)
	if err != nil {
		return fmt.Errorf("create container: %w", err)
	}
	return nil
}

// GetContainerByID returns a container by primary key.
func (db *DB) GetContainerByID(id string) (*Container, error) {
	c, err := scanContainer(db.QueryRow(`SELECT `+containerColumns+` FROM containers WHERE id = ?`, id))
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
	rows, err := db.Query(`SELECT `+containerColumns+` FROM containers WHERE owner_id = ? ORDER BY created_at DESC`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	defer rows.Close()

	var containers []*Container
	for rows.Next() {
		c, err := scanContainer(rows)
		if err != nil {
			return nil, fmt.Errorf("scan container: %w", err)
		}
		containers = append(containers, c)
	}
	return containers, rows.Err()
}

// GetContainerByName returns a container by name and ownerID.
func (db *DB) GetContainerByName(name, ownerID string) (*Container, error) {
	c, err := scanContainer(db.QueryRow(`SELECT `+containerColumns+` FROM containers WHERE name = ? AND owner_id = ?`, name, ownerID))
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
	c, err := scanContainer(db.QueryRow(`SELECT `+containerColumns+` FROM containers WHERE name = ?`, name))
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
	rows, err := db.Query(`SELECT ` + containerColumns + ` FROM containers ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list all containers: %w", err)
	}
	defer rows.Close()

	var containers []*Container
	for rows.Next() {
		c, err := scanContainer(rows)
		if err != nil {
			return nil, fmt.Errorf("scan container: %w", err)
		}
		containers = append(containers, c)
	}
	return containers, rows.Err()
}

// RenameContainer updates the display name of a container.
func (db *DB) RenameContainer(id, newName string) error {
	_, err := db.Exec(
		`UPDATE containers SET name = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		newName, id,
	)
	if err != nil {
		return fmt.Errorf("rename container: %w", err)
	}
	return nil
}

// DeleteContainer removes a container record by ID.
func (db *DB) DeleteContainer(id string) error {
	_, err := db.Exec(`DELETE FROM containers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete container: %w", err)
	}
	return nil
}
