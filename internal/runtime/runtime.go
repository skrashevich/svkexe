package runtime

import (
	"context"
	"io"
	"time"
)

// Container represents a running or stopped container instance.
type Container struct {
	ID      string
	Name    string
	Status  string
	OwnerID string
	IP      string
	// MAC is the hardware address of the interface IP was found on. The
	// metadata service reports it, since EC2's tooling keys its network tree on
	// it. A runtime that cannot say leaves it empty, and the key is then simply
	// not published rather than invented.
	MAC string
	// AddressFiltered reports whether the runtime is stopping this instance from
	// sending as any address but its own. Anything that treats a source address
	// as an identity has to know: a tenant is root inside their own VM and can
	// otherwise claim a neighbour's address. A runtime that cannot say leaves it
	// false, which is what makes the answer safe to act on by default.
	AddressFiltered bool
	CreatedAt       time.Time
}

// CreateOpts holds parameters for creating a new container.
type CreateOpts struct {
	Name     string
	OwnerID  string
	Image    string
	CPULimit int
	MemoryMB int
	DiskGB   int
	// Nesting allows containers of the container's own — Docker, buildah, a
	// nested Incus. It is written explicitly rather than left to the profile so
	// that the answer travels with the VM and does not change under it when the
	// host profile is edited.
	Nesting bool
}

// ContainerRuntime defines the interface for managing containers.
type ContainerRuntime interface {
	Create(ctx context.Context, opts CreateOpts) (*Container, error)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error
	Delete(ctx context.Context, id string) error
	Get(ctx context.Context, id string) (*Container, error)
	List(ctx context.Context, ownerID string) ([]*Container, error)
	Exec(ctx context.Context, id string, cmd []string) ([]byte, error)
	Snapshot(ctx context.Context, id string, name string) error
	// SetNesting writes security.nesting onto an existing instance. The runtime
	// reads it when the container boots, so this call decides what the next
	// start puts in effect rather than changing a running container.
	SetNesting(ctx context.Context, id string, enabled bool) error
}

// ExecInteractiveOpts holds parameters for an interactive PTY exec session.
type ExecInteractiveOpts struct {
	// IncusName is the Incus container name (not the DB id).
	IncusName string
	// Command to run inside the container.
	Command []string
	// Env is an optional set of environment variables to pass to the command.
	Env map[string]string
	// Stdin is where the container reads input from.
	Stdin io.Reader
	// Stdout is where the container writes output to.
	Stdout io.Writer
	// InitialCols/Rows set the starting PTY size.
	InitialCols uint16
	InitialRows uint16
	// Resize is an optional channel the caller can send resize events to.
	// Send a ResizeEvent; close the channel to signal end.
	Resize <-chan ResizeEvent
	// Done is closed by the runtime when the exec session ends.
	Done chan struct{}
}

// ResizeEvent carries new terminal dimensions.
type ResizeEvent struct {
	Cols uint16
	Rows uint16
}

// ShellRuntime is implemented by runtimes that support interactive PTY sessions.
type ShellRuntime interface {
	ExecInteractive(ctx context.Context, opts ExecInteractiveOpts) error
}

// FileRuntime is implemented by runtimes that support file push/pull.
type FileRuntime interface {
	PullFile(ctx context.Context, id, path string) ([]byte, error)
	PushFile(ctx context.Context, id, path string, data []byte) error
}
