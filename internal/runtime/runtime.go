package runtime

import (
	"context"
	"io"
	"time"
)

// Container represents a running or stopped container instance.
type Container struct {
	ID        string
	Name      string
	Status    string
	OwnerID   string
	IP        string
	CreatedAt time.Time
}

// CreateOpts holds parameters for creating a new container.
type CreateOpts struct {
	Name     string
	OwnerID  string
	Image    string
	CPULimit int
	MemoryMB int
	DiskGB   int
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
