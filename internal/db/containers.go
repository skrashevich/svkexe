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

	// MaxInitialTaskLen bounds the free-text task handed to the agent.
	MaxInitialTaskLen = 4000

	// DefaultNesting is the answer a VM gets when nobody chose one. It exists so
	// the schema default, the migration backfill and every creation path say the
	// same thing; nothing would catch them drifting apart.
	DefaultNesting = true

	// TaskPending means the task still has to reach the agent.
	TaskPending = "pending"
	// TaskSent means the agent accepted the task and opened a conversation.
	TaskSent = "sent"
	// TaskWorking means the agent is currently running the task's turn.
	TaskWorking = "working"
	// TaskDone means the agent finished the turn without an error.
	TaskDone = "done"
	// TaskFailed means delivery failed or the agent ended its turn on an error;
	// the owner has to retry explicitly.
	TaskFailed = "failed"
)

// TaskInProgress reports whether a task state is still expected to change on
// its own, i.e. whether it is worth polling the agent for progress.
func TaskInProgress(state string) bool {
	return state == TaskSent || state == TaskWorking
}

// Container represents a managed Incus container.
type Container struct {
	// SharedAccess is presentation-only; authorization always reads the database.
	SharedAccess bool

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
	// InitialTask is free text handed to the agent once, right after the VM
	// first comes up.
	InitialTask string
	// InitialTaskState is empty when no task was requested, otherwise
	// TaskPending, TaskSent, TaskWorking, TaskDone or TaskFailed.
	InitialTaskState string
	// InitialTaskError explains the last failed delivery, or the error the
	// agent ended its turn on.
	InitialTaskError string
	// InitialTaskConversation is the agent conversation the task runs in. It is
	// what progress polling watches, so it is empty until delivery succeeded.
	InitialTaskConversation string
	// InitialTaskResumes counts the times the gateway has asked the agent to
	// pick this task back up after a transient failure. It is the resume
	// budget, and the gateway owns it: a resume the agent declines leaves the
	// agent's own message log unchanged, so nothing there could bound it.
	InitialTaskResumes int
	// Nesting is the owner's answer to "may this VM run containers of its own".
	// It is only half of the story: NestingAllowed is the ceiling.
	Nesting bool
	// NestingApplied is what the running instance actually booted with. LXC
	// reads security.nesting at container start, so a change made against a
	// running VM is only a promise until it restarts.
	NestingApplied bool
	CreatedAt      time.Time
	UpdatedAt      time.Time

	// Aliases holds the VM's custom hostnames. It is not a column: reads leave
	// it nil and callers that need it ask for it explicitly via AttachAliases,
	// so request routing does not pay for a join it never looks at.
	Aliases []*ContainerAlias

	// NestingAllowed mirrors the deployment-wide ceiling. Like Aliases it is not
	// a column: a caller that renders the VM asks for it through
	// AttachNestingPolicy. A caller that never asked leaves it false, so a VM
	// cannot be rendered as nesting-capable by omission — which is why nothing
	// may treat this field as authoritative. NestingEffective, and everything
	// built on it, answers only for a Container the caller has filled in.
	NestingAllowed bool
}

// NestingEffective reports whether nested containers are meant to work in this
// VM: the owner has to want it and the deployment has to allow it.
func (c *Container) NestingEffective() bool {
	return c.NestingAllowed && c.Nesting
}

// NestingPending reports that the VM is running with a nesting setting other
// than the one now in force, i.e. that the owner still owes it a restart.
// A VM that is not running has nothing pending: whatever is stored takes effect
// the moment it next boots.
func (c *Container) NestingPending() bool {
	return strings.EqualFold(c.Status, "running") && c.NestingApplied != c.NestingEffective()
}

// containerColumns keeps every read of a container in sync.
const containerColumns = `id, name, owner_id, incus_name, status, COALESCE(ip_address,''), cpu_limit, memory_mb, disk_gb, app_port, app_public, initial_task, initial_task_state, initial_task_error, initial_task_conversation, initial_task_resumes, nesting, nesting_applied, created_at, updated_at`

func scanContainer(row interface{ Scan(...any) error }) (*Container, error) {
	c := &Container{}
	err := row.Scan(&c.ID, &c.Name, &c.OwnerID, &c.IncusName, &c.Status, &c.IPAddress,
		&c.CPULimit, &c.MemoryMB, &c.DiskGB, &c.AppPort, &c.AppPublic,
		&c.InitialTask, &c.InitialTaskState, &c.InitialTaskError, &c.InitialTaskConversation,
		&c.InitialTaskResumes, &c.Nesting, &c.NestingApplied, &c.CreatedAt, &c.UpdatedAt)
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

// UpdateContainerNesting stores whether the owner wants nested containers here.
// It leaves nesting_applied alone on purpose: the change is not in effect until
// the VM boots again, and pretending otherwise would hide the restart the owner
// still owes from the dashboard.
func (db *DB) UpdateContainerNesting(id string, nesting bool) error {
	_, err := db.Exec(
		`UPDATE containers SET nesting = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		nesting, id,
	)
	if err != nil {
		return fmt.Errorf("update container nesting: %w", err)
	}
	return nil
}

// SetNestingApplied records what the instance will actually boot with. It is
// called on the paths that start or build a container, once the setting has
// reached Incus, because that is the moment the wish becomes the truth.
func (db *DB) SetNestingApplied(id string, applied bool) error {
	_, err := db.Exec(
		`UPDATE containers SET nesting_applied = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		applied, id,
	)
	if err != nil {
		return fmt.Errorf("record applied nesting: %w", err)
	}
	return nil
}

// AttachNestingPolicy fills in the deployment-wide ceiling on the given VMs, so
// that a card can answer "is nesting on here" without every caller having to
// know the setting exists. One read serves the whole list.
//
// A ceiling that cannot be read is attached as allowed, and the error is
// returned alongside it — the opposite of how vmconfig.EffectiveNesting resolves
// the same failure, deliberately. This value only decides what a page displays
// and offers, and displaying grants nothing: every path that acts on the answer
// resolves it again, fail-closed, before anything reaches Incus. Rendering
// "off" here would instead tell the owner an administrator forbade nesting when
// none did, and — worse — would compute NestingPending as false for a VM that
// genuinely still owes a restart, hiding the one prompt that would fix it.
func (db *DB) AttachNestingPolicy(containers ...*Container) error {
	allowed, err := db.NestingAllowed()
	if err != nil {
		allowed = true
	}
	for _, c := range containers {
		if c != nil {
			c.NestingAllowed = allowed
		}
	}
	return err
}

// CreateContainer inserts a new container record.
func (db *DB) CreateContainer(c *Container) error {
	if c.AppPort == 0 {
		c.AppPort = DefaultAppPort
	}
	if !ValidAppPort(c.AppPort) {
		return fmt.Errorf("invalid app port %d", c.AppPort)
	}
	c.InitialTask = strings.TrimSpace(c.InitialTask)
	if len(c.InitialTask) > MaxInitialTaskLen {
		return fmt.Errorf("task must be at most %d characters", MaxInitialTaskLen)
	}
	// Asking for a task is what queues it; a VM without one must stay inert.
	if c.InitialTask != "" {
		c.InitialTaskState = TaskPending
	} else {
		c.InitialTaskState = ""
	}
	_, err := db.Exec(
		`INSERT INTO containers (id, name, owner_id, incus_name, status, ip_address, cpu_limit, memory_mb, disk_gb, app_port, app_public, initial_task, initial_task_state, nesting)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.OwnerID, c.IncusName, c.Status, c.IPAddress,
		c.CPULimit, c.MemoryMB, c.DiskGB, c.AppPort, c.AppPublic,
		c.InitialTask, c.InitialTaskState, c.Nesting,
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

// GetContainerByIncusName returns a container by the Incus instance name.
//
// The metadata service needs it: the container runtime is the authority on which
// instance currently holds a given address, and the name is all it can say. A
// lookup by the platform's own ip_address column would instead trust a value
// that is only refreshed when something happens to the VM, and a stale one would
// hand a caller somebody else's identity.
func (db *DB) GetContainerByIncusName(incusName string) (*Container, error) {
	c, err := scanContainer(db.QueryRow(`SELECT `+containerColumns+` FROM containers WHERE incus_name = ?`, incusName))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sql.ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("get container by incus name: %w", err)
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

// DeleteContainer removes a container record by ID, together with the custom
// hostnames pointed at it. The aliases are deleted explicitly rather than left
// to ON DELETE CASCADE: the foreign-key pragma is set on whichever pooled
// connection Open happened to use, so cascading is not something every later
// query can count on. A surviving alias row would keep a dead VM's hostname
// claimed and unusable by anyone else.
func (db *DB) DeleteContainer(id string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("delete container: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM container_access WHERE container_id = ?`, id); err != nil {
		return fmt.Errorf("delete container access: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM container_aliases WHERE container_id = ?`, id); err != nil {
		return fmt.Errorf("delete container aliases: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM containers WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete container: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete container: %w", err)
	}
	return nil
}

// SetInitialTaskState records the outcome of a delivery attempt.
func (db *DB) SetInitialTaskState(id, state, reason string) error {
	_, err := db.Exec(
		`UPDATE containers SET initial_task_state = ?, initial_task_error = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		state, reason, id,
	)
	if err != nil {
		return fmt.Errorf("update initial task state: %w", err)
	}
	return nil
}

// SetInitialTaskDelivered records that the agent accepted the task, together
// with the conversation progress polling has to watch.
func (db *DB) SetInitialTaskDelivered(id, conversationID string) error {
	_, err := db.Exec(
		`UPDATE containers SET initial_task_state = ?, initial_task_error = '', initial_task_conversation = ?, initial_task_resumes = 0, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		TaskSent, conversationID, id,
	)
	if err != nil {
		return fmt.Errorf("record initial task delivery: %w", err)
	}
	return nil
}

// SetInitialTaskConversation records the conversation a delivered task turned
// out to run in, without touching its state. It only fills a gap: a task whose
// conversation is already known is left alone.
func (db *DB) SetInitialTaskConversation(id, conversationID string) error {
	res, err := db.Exec(
		`UPDATE containers SET initial_task_conversation = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND initial_task_conversation = ''`,
		conversationID, id,
	)
	if err != nil {
		return fmt.Errorf("record initial task conversation: %w", err)
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("record initial task conversation: %w", err)
	}
	// Saying nothing here would leave the caller believing a conversation it
	// does not have, and polling one the VM never agreed to.
	if changed == 0 {
		return fmt.Errorf("no task waiting for conversation %q", conversationID)
	}
	return nil
}

// CountInitialTaskResume records that the gateway has asked the agent to pick a
// task back up, and returns the new total. This count is the resume budget, and
// the gateway keeps it because nothing in the agent's own data could: a resume
// the agent declines leaves its message log untouched.
func (db *DB) CountInitialTaskResume(id string) (int, error) {
	var resumes int
	err := db.QueryRow(
		`UPDATE containers SET initial_task_resumes = initial_task_resumes + 1, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? RETURNING initial_task_resumes`, id,
	).Scan(&resumes)
	if err != nil {
		return 0, fmt.Errorf("count initial task resume: %w", err)
	}
	return resumes, nil
}

// ListContainersWithTaskInProgress returns the running VMs whose task is still
// expected to progress, i.e. the ones worth polling the agent about. A VM whose
// conversation was never recorded is included too: the gateway has to look that
// conversation up rather than leave the task reported as starting forever.
func (db *DB) ListContainersWithTaskInProgress() ([]*Container, error) {
	rows, err := db.Query(
		`SELECT `+containerColumns+` FROM containers
		 WHERE initial_task_state IN (?, ?) AND status = 'running'
		 ORDER BY updated_at`,
		TaskSent, TaskWorking,
	)
	if err != nil {
		return nil, fmt.Errorf("list containers with a running task: %w", err)
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

// RetryInitialTask re-queues a failed task. Delivery never retries on its own,
// so a task written weeks ago cannot fire on an unrelated restart.
func (db *DB) RetryInitialTask(id string) error {
	res, err := db.Exec(
		`UPDATE containers SET initial_task_state = ?, initial_task_error = '', initial_task_conversation = '', initial_task_resumes = 0, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND initial_task != '' AND initial_task_state = ?`,
		TaskPending, id, TaskFailed,
	)
	if err != nil {
		return fmt.Errorf("retry initial task: %w", err)
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("retry initial task: %w", err)
	}
	if changed == 0 {
		return fmt.Errorf("no failed task to retry")
	}
	return nil
}
