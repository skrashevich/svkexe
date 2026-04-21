package runtime

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// MockRuntime is a mock implementation of ContainerRuntime for testing.
type MockRuntime struct {
	containers map[string]*Container
}

func NewMockRuntime() *MockRuntime {
	return &MockRuntime{
		containers: make(map[string]*Container),
	}
}

func (m *MockRuntime) Create(ctx context.Context, opts CreateOpts) (*Container, error) {
	name := containerName(opts.OwnerID, opts.Name)
	c := &Container{
		ID:        name,
		Name:      name,
		Status:    "stopped",
		OwnerID:   opts.OwnerID,
		IP:        "",
		CreatedAt: time.Now(),
	}
	m.containers[name] = c
	return c, nil
}

func (m *MockRuntime) Start(ctx context.Context, id string) error {
	c, ok := m.containers[id]
	if !ok {
		return ErrNotFound(id)
	}
	c.Status = "running"
	c.IP = "10.0.0.1"
	return nil
}

func (m *MockRuntime) Stop(ctx context.Context, id string) error {
	c, ok := m.containers[id]
	if !ok {
		return ErrNotFound(id)
	}
	c.Status = "stopped"
	c.IP = ""
	return nil
}

func (m *MockRuntime) Delete(ctx context.Context, id string) error {
	if _, ok := m.containers[id]; !ok {
		return ErrNotFound(id)
	}
	delete(m.containers, id)
	return nil
}

func (m *MockRuntime) Get(ctx context.Context, id string) (*Container, error) {
	c, ok := m.containers[id]
	if !ok {
		return nil, ErrNotFound(id)
	}
	return c, nil
}

func (m *MockRuntime) List(ctx context.Context, ownerID string) ([]*Container, error) {
	var result []*Container
	for _, c := range m.containers {
		if ownerID == "" || c.OwnerID == ownerID {
			result = append(result, c)
		}
	}
	return result, nil
}

func (m *MockRuntime) Exec(ctx context.Context, id string, cmd []string) ([]byte, error) {
	if _, ok := m.containers[id]; !ok {
		return nil, ErrNotFound(id)
	}
	return []byte("mock output"), nil
}

func (m *MockRuntime) Snapshot(ctx context.Context, id string, name string) error {
	if _, ok := m.containers[id]; !ok {
		return ErrNotFound(id)
	}
	return nil
}

// ErrNotFound returns an error for a missing container.
func ErrNotFound(id string) error {
	return fmt.Errorf("container not found: %s", id)
}

// Verify MockRuntime satisfies ContainerRuntime interface at compile time.
var _ ContainerRuntime = (*MockRuntime)(nil)

// Tests

func TestMockRuntime_CreateAndGet(t *testing.T) {
	ctx := context.Background()
	m := NewMockRuntime()

	opts := CreateOpts{
		Name:     "test",
		OwnerID:  "user1",
		Image:    "ubuntu/22.04",
		CPULimit: 1,
		MemoryMB: 512,
		DiskGB:   10,
	}

	c, err := m.Create(ctx, opts)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if c.OwnerID != "user1" {
		t.Errorf("expected ownerID user1, got %s", c.OwnerID)
	}
	if c.Status != "stopped" {
		t.Errorf("expected status stopped, got %s", c.Status)
	}

	got, err := m.Get(ctx, c.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.ID != c.ID {
		t.Errorf("expected ID %s, got %s", c.ID, got.ID)
	}
}

func TestMockRuntime_StartStop(t *testing.T) {
	ctx := context.Background()
	m := NewMockRuntime()

	c, _ := m.Create(ctx, CreateOpts{Name: "web", OwnerID: "u1", Image: "debian/12"})

	if err := m.Start(ctx, c.ID); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	got, _ := m.Get(ctx, c.ID)
	if got.Status != "running" {
		t.Errorf("expected running, got %s", got.Status)
	}

	if err := m.Stop(ctx, c.ID); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	got, _ = m.Get(ctx, c.ID)
	if got.Status != "stopped" {
		t.Errorf("expected stopped, got %s", got.Status)
	}
}

func TestMockRuntime_Delete(t *testing.T) {
	ctx := context.Background()
	m := NewMockRuntime()

	c, _ := m.Create(ctx, CreateOpts{Name: "tmp", OwnerID: "u2", Image: "alpine/3.19"})

	if err := m.Delete(ctx, c.ID); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, err := m.Get(ctx, c.ID)
	if err == nil {
		t.Error("expected error after deletion, got nil")
	}
}

func TestMockRuntime_List(t *testing.T) {
	ctx := context.Background()
	m := NewMockRuntime()

	m.Create(ctx, CreateOpts{Name: "a", OwnerID: "alice", Image: "ubuntu/22.04"})
	m.Create(ctx, CreateOpts{Name: "b", OwnerID: "alice", Image: "ubuntu/22.04"})
	m.Create(ctx, CreateOpts{Name: "c", OwnerID: "bob", Image: "ubuntu/22.04"})

	aliceContainers, err := m.List(ctx, "alice")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(aliceContainers) != 2 {
		t.Errorf("expected 2 containers for alice, got %d", len(aliceContainers))
	}

	all, _ := m.List(ctx, "")
	if len(all) != 3 {
		t.Errorf("expected 3 total containers, got %d", len(all))
	}
}

func TestMockRuntime_Exec(t *testing.T) {
	ctx := context.Background()
	m := NewMockRuntime()

	c, _ := m.Create(ctx, CreateOpts{Name: "exec-test", OwnerID: "u3", Image: "ubuntu/22.04"})

	out, err := m.Exec(ctx, c.ID, []string{"echo", "hello"})
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	if string(out) != "mock output" {
		t.Errorf("unexpected output: %s", string(out))
	}
}

func TestMockRuntime_Snapshot(t *testing.T) {
	ctx := context.Background()
	m := NewMockRuntime()

	c, _ := m.Create(ctx, CreateOpts{Name: "snap-test", OwnerID: "u4", Image: "ubuntu/22.04"})

	if err := m.Snapshot(ctx, c.ID, "snap1"); err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}
}

func TestMockRuntime_NotFound(t *testing.T) {
	ctx := context.Background()
	m := NewMockRuntime()

	if err := m.Start(ctx, "nonexistent"); err == nil {
		t.Error("expected error for nonexistent container")
	}
	if err := m.Stop(ctx, "nonexistent"); err == nil {
		t.Error("expected error for nonexistent container")
	}
	if err := m.Delete(ctx, "nonexistent"); err == nil {
		t.Error("expected error for nonexistent container")
	}
	if _, err := m.Exec(ctx, "nonexistent", []string{"ls"}); err == nil {
		t.Error("expected error for nonexistent container")
	}
}
