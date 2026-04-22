package runtime

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	incus "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"
)

const containerPrefix = "svkexe"

// waitOp waits for an Incus operation to complete, respecting context cancellation.
func waitOp(ctx context.Context, op incus.Operation) error {
	done := make(chan error, 1)
	go func() { done <- op.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = op.Cancel()
		return ctx.Err()
	}
}

// IncusRuntime implements ContainerRuntime using the Incus container runtime.
type IncusRuntime struct {
	client incus.InstanceServer
}

// NewIncusRuntime connects to Incus via unix socket and returns a new IncusRuntime.
func NewIncusRuntime(socketPath string) (*IncusRuntime, error) {
	if socketPath == "" {
		socketPath = "/var/lib/incus/unix.socket"
	}
	c, err := incus.ConnectIncusUnix(socketPath, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to incus socket %s: %w", socketPath, err)
	}
	return &IncusRuntime{client: c}, nil
}

// containerName returns the canonical Incus container name for a given owner and name.
func containerName(ownerID, name string) string {
	return fmt.Sprintf("%s-%s-%s", containerPrefix, ownerID, name)
}

// Create creates a new container with the given options.
func (r *IncusRuntime) Create(ctx context.Context, opts CreateOpts) (*Container, error) {
	name := containerName(opts.OwnerID, opts.Name)

	devices := map[string]map[string]string{}
	if opts.DiskGB > 0 {
		devices["root"] = map[string]string{
			"type": "disk",
			"path": "/",
			"pool": "svkexe-pool",
			"size": fmt.Sprintf("%dGB", opts.DiskGB),
		}
	}

	config := map[string]string{
		"user.owner_id": opts.OwnerID,
	}
	if opts.CPULimit > 0 {
		config["limits.cpu"] = fmt.Sprintf("%d", opts.CPULimit)
	}
	if opts.MemoryMB > 0 {
		config["limits.memory"] = fmt.Sprintf("%dMB", opts.MemoryMB)
	}

	req := api.InstancesPost{
		Name: name,
		Source: api.InstanceSource{
			Type:  "image",
			Alias: opts.Image,
		},
		InstancePut: api.InstancePut{
			Profiles: []string{"svkexe-default"},
			Config:   config,
			Devices:  devices,
		},
	}

	op, err := r.client.CreateInstance(req)
	if err != nil {
		return nil, fmt.Errorf("create container %s: %w", name, err)
	}
	if err := waitOp(ctx, op); err != nil {
		return nil, fmt.Errorf("wait for container creation %s: %w", name, err)
	}

	return r.Get(ctx, name)
}

// Start starts an existing container.
func (r *IncusRuntime) Start(ctx context.Context, id string) error {
	req := api.InstanceStatePut{
		Action:  "start",
		Timeout: 30,
	}
	op, err := r.client.UpdateInstanceState(id, req, "")
	if err != nil {
		return fmt.Errorf("start container %s: %w", id, err)
	}
	if err := waitOp(ctx, op); err != nil {
		return fmt.Errorf("wait for container start %s: %w", id, err)
	}
	return nil
}

// Stop stops a running container.
func (r *IncusRuntime) Stop(ctx context.Context, id string) error {
	req := api.InstanceStatePut{
		Action:  "stop",
		Timeout: 30,
		Force:   false,
	}
	op, err := r.client.UpdateInstanceState(id, req, "")
	if err != nil {
		return fmt.Errorf("stop container %s: %w", id, err)
	}
	if err := waitOp(ctx, op); err != nil {
		return fmt.Errorf("wait for container stop %s: %w", id, err)
	}
	return nil
}

// Delete deletes a container. The container must be stopped first.
func (r *IncusRuntime) Delete(ctx context.Context, id string) error {
	op, err := r.client.DeleteInstance(id)
	if err != nil {
		return fmt.Errorf("delete container %s: %w", id, err)
	}
	if err := waitOp(ctx, op); err != nil {
		return fmt.Errorf("wait for container deletion %s: %w", id, err)
	}
	return nil
}

// Get retrieves a container by its Incus name/ID.
func (r *IncusRuntime) Get(ctx context.Context, id string) (*Container, error) {
	inst, _, err := r.client.GetInstance(id)
	if err != nil {
		return nil, fmt.Errorf("get container %s: %w", id, err)
	}

	state, _, err := r.client.GetInstanceState(id)
	if err != nil {
		return nil, fmt.Errorf("get container state %s: %w", id, err)
	}

	ip := extractIP(state)
	ownerID := inst.Config["user.owner_id"]

	return &Container{
		ID:        inst.Name,
		Name:      inst.Name,
		Status:    strings.ToLower(inst.Status),
		OwnerID:   ownerID,
		IP:        ip,
		CreatedAt: inst.CreatedAt,
	}, nil
}

// List returns all containers belonging to a specific owner.
// If ownerID is empty, all svkexe containers are returned.
func (r *IncusRuntime) List(ctx context.Context, ownerID string) ([]*Container, error) {
	instances, err := r.client.GetInstances(api.InstanceTypeContainer)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	var result []*Container
	for _, inst := range instances {
		if !strings.HasPrefix(inst.Name, containerPrefix+"-") {
			continue
		}
		if ownerID != "" && inst.Config["user.owner_id"] != ownerID {
			continue
		}
		state, _, err := r.client.GetInstanceState(inst.Name)
		if err != nil {
			continue
		}
		ip := extractIP(state)
		result = append(result, &Container{
			ID:        inst.Name,
			Name:      inst.Name,
			Status:    strings.ToLower(inst.Status),
			OwnerID:   inst.Config["user.owner_id"],
			IP:        ip,
			CreatedAt: inst.CreatedAt,
		})
	}
	return result, nil
}

// Exec runs a command inside a container and returns stdout output.
func (r *IncusRuntime) Exec(ctx context.Context, id string, cmd []string) ([]byte, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	req := api.InstanceExecPost{
		Command:     cmd,
		WaitForWS:   true,
		Interactive: false,
	}

	args := incus.InstanceExecArgs{
		Stdout: &stdout,
		Stderr: &stderr,
	}

	op, err := r.client.ExecInstance(id, req, &args)
	if err != nil {
		return nil, fmt.Errorf("exec in container %s: %w", id, err)
	}
	if err := op.Wait(); err != nil {
		return nil, fmt.Errorf("wait for exec in container %s: %w", id, err)
	}
	return stdout.Bytes(), nil
}

// Snapshot creates a snapshot of a container.
func (r *IncusRuntime) Snapshot(ctx context.Context, id string, name string) error {
	req := api.InstanceSnapshotsPost{
		Name:     name,
		Stateful: false,
	}
	op, err := r.client.CreateInstanceSnapshot(id, req)
	if err != nil {
		return fmt.Errorf("snapshot container %s: %w", id, err)
	}
	if err := waitOp(ctx, op); err != nil {
		return fmt.Errorf("wait for snapshot %s on container %s: %w", name, id, err)
	}
	return nil
}

// PullFile reads a file from inside a container and returns its contents.
func (r *IncusRuntime) PullFile(ctx context.Context, id, path string) ([]byte, error) {
	reader, _, err := r.client.GetInstanceFile(id, path)
	if err != nil {
		return nil, fmt.Errorf("pull file %s from %s: %w", path, id, err)
	}
	defer reader.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(reader); err != nil {
		return nil, fmt.Errorf("read file %s from %s: %w", path, id, err)
	}
	return buf.Bytes(), nil
}

// PushFile writes data to a file inside a container.
func (r *IncusRuntime) PushFile(ctx context.Context, id, path string, data []byte) error {
	args := incus.InstanceFileArgs{
		Content:   bytes.NewReader(data),
		UID:       0,
		GID:       0,
		Mode:      0644,
		Type:      "file",
		WriteMode: "overwrite",
	}
	if err := r.client.CreateInstanceFile(id, path, args); err != nil {
		return fmt.Errorf("push file %s to %s: %w", path, id, err)
	}
	return nil
}

// extractIP returns the first IPv4 address from instance state network info.
func extractIP(state *api.InstanceState) string {
	if state == nil || state.Network == nil {
		return ""
	}
	for iface, net := range state.Network {
		if iface == "lo" {
			continue
		}
		for _, addr := range net.Addresses {
			if addr.Family == "inet" && addr.Scope == "global" {
				return addr.Address
			}
		}
	}
	return ""
}

// ExecInteractive starts an interactive PTY session inside a container.
// It implements ShellRuntime. The call blocks until the session ends or ctx is cancelled.
func (r *IncusRuntime) ExecInteractive(ctx context.Context, opts ExecInteractiveOpts) error {
	cols := opts.InitialCols
	rows := opts.InitialRows
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}

	cmd := opts.Command
	if len(cmd) == 0 {
		cmd = []string{"/bin/bash"}
	}

	req := api.InstanceExecPost{
		Command:     cmd,
		WaitForWS:   true,
		Interactive: true,
		Width:       int(cols),
		Height:      int(rows),
		Environment: opts.Env,
	}

	// controlConn is set once the Control callback fires; protected by mu.
	var mu sync.Mutex
	var controlConn *websocket.Conn
	controlReady := make(chan struct{})

	dataDone := make(chan bool, 1)

	execArgs := incus.InstanceExecArgs{
		Stdin:    opts.Stdin,
		Stdout:   opts.Stdout,
		Stderr:   opts.Stdout,
		DataDone: dataDone,
		Control: func(conn *websocket.Conn) {
			mu.Lock()
			controlConn = conn
			mu.Unlock()
			close(controlReady)
			// Block until data is done (keeps the control goroutine alive).
			<-dataDone
		},
	}

	op, err := r.client.ExecInstance(opts.IncusName, req, &execArgs)
	if err != nil {
		return fmt.Errorf("exec interactive in %s: %w", opts.IncusName, err)
	}

	// Forward resize events once the control channel is ready.
	if opts.Resize != nil {
		go func() {
			select {
			case <-controlReady:
			case <-ctx.Done():
				return
			}
			for {
				select {
				case ev, ok := <-opts.Resize:
					if !ok {
						return
					}
					mu.Lock()
					cc := controlConn
					mu.Unlock()
					if cc == nil {
						continue
					}
					msg := api.InstanceExecControl{
						Command: "window-resize",
						Args: map[string]string{
							"width":  strconv.Itoa(int(ev.Cols)),
							"height": strconv.Itoa(int(ev.Rows)),
						},
					}
					_ = cc.WriteJSON(msg)
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// Signal caller when done.
	if opts.Done != nil {
		go func() {
			_ = op.Wait()
			close(opts.Done)
		}()
	} else {
		if err := op.Wait(); err != nil {
			return fmt.Errorf("exec interactive wait %s: %w", opts.IncusName, err)
		}
	}

	return nil
}

