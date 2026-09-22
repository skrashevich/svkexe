package sshgw

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
)

// run executes one command line the way a one-shot "ssh host <command>"
// invocation would, and returns what the caller would have seen.
func run(t *testing.T, s *Server, user *db.User, line string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	err := s.exec(t.Context(), nil, newOut(&buf, false), user, line, false, false)
	return buf.String(), err
}

// runInteractive executes one command line as if it had been typed at the
// menu's prompt, which some commands treat differently from a one-shot.
func runInteractive(t *testing.T, s *Server, user *db.User, line string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	err := s.exec(t.Context(), nil, newOut(&buf, false), user, line, true, false)
	return buf.String(), err
}

type recreateRuntime struct {
	runtime.ContainerRuntime
	steps      []string
	failBackup bool
}

func (r *recreateRuntime) Start(context.Context, string) error {
	r.steps = append(r.steps, "start")
	return nil
}
func (r *recreateRuntime) Stop(context.Context, string) error {
	r.steps = append(r.steps, "stop")
	return nil
}
func (r *recreateRuntime) Delete(context.Context, string) error {
	r.steps = append(r.steps, "delete")
	return nil
}
func (r *recreateRuntime) Create(context.Context, runtime.CreateOpts) (*runtime.Container, error) {
	r.steps = append(r.steps, "create")
	return &runtime.Container{}, nil
}
func (r *recreateRuntime) Get(context.Context, string) (*runtime.Container, error) {
	return &runtime.Container{IP: "10.0.0.2"}, nil
}

// Recreate boots the old instance to back it up and then builds a new one, so
// both of those have to carry the current nesting setting; the step is recorded
// to keep it in the ordering this test asserts on.
func (r *recreateRuntime) SetNesting(context.Context, string, bool) error {
	r.steps = append(r.steps, "nesting")
	return nil
}
func (r *recreateRuntime) PullFile(context.Context, string, string) ([]byte, error) {
	return []byte("backup"), nil
}
func (r *recreateRuntime) PushFile(_ context.Context, _, path string, _ []byte) error {
	r.steps = append(r.steps, "push "+path)
	return nil
}
func (r *recreateRuntime) Exec(_ context.Context, _ string, cmd []string) ([]byte, error) {
	text := strings.Join(cmd, " ")
	r.steps = append(r.steps, text)
	if r.failBackup && strings.Contains(text, "tar -czf") {
		return nil, errors.New("backup failed")
	}
	if strings.Contains(text, "is-active") {
		return []byte("active"), nil
	}
	if len(cmd) == 2 && cmd[1] == "version" {
		return []byte(`{"version":"picoclaw-v0.3.1-svkexe","customized":true}`), nil
	}
	return nil, nil
}
func TestSSHRecreatePreservesDataBeforeStartingAgent(t *testing.T) {
	for _, failBackup := range []bool{false, true} {
		t.Run(map[bool]string{false: "restore_before_setup", true: "failed_backup_keeps_vm"}[failBackup], func(t *testing.T) {
			database, err := db.Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			user := &db.User{ID: "u", Email: "u@example.test", Role: "user"}
			if err := database.CreateUser(user); err != nil {
				t.Fatal(err)
			}
			if err := database.CreateContainer(&db.Container{ID: "vm", Name: "dev", IncusName: "incus-dev", OwnerID: user.ID, Status: "running"}); err != nil {
				t.Fatal(err)
			}
			binary := filepath.Join(t.TempDir(), "picoclaw")
			if err := os.WriteFile(binary, []byte("agent"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SVKEXE_AGENT_BINARY", binary)
			rt := &recreateRuntime{failBackup: failBackup}
			s := &Server{db: database, runtime: rt}
			output, _ := run(t, s, user, "recreate dev --force")
			steps := strings.Join(rt.steps, "\n")
			c, err := database.GetContainerByID("vm")
			if err != nil {
				t.Fatal(err)
			}
			if failBackup {
				if strings.Contains(steps, "\ndelete\n") || c.Status != "error" {
					t.Fatalf("failed backup did not abort: %s", steps)
				}
				return
			}
			restore, agent := strings.Index(steps, "tar -xzf"), strings.Index(steps, "enable picoclaw.service")
			if restore < 0 || agent < 0 || restore > agent || c.Status != "running" {
				t.Fatalf("bad restore/start order or status %s: %s\n%s", c.Status, steps, output)
			}
		})
	}
}

// Recreate boots the old instance to take the backup. That is a real start,
// running for as long as the tar takes, so it has to carry the current nesting
// setting: leaving it out would let a VM come up with a capability an operator
// has since revoked, and the window is a whole backup long.
func TestSSHRecreateAppliesNestingBeforeTheBackupBoot(t *testing.T) {
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	user := &db.User{ID: "u", Email: "u@example.test", Role: "user"}
	if err := database.CreateUser(user); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateContainer(&db.Container{
		ID: "vm", Name: "dev", IncusName: "incus-dev", OwnerID: user.ID, Status: "stopped", Nesting: true,
	}); err != nil {
		t.Fatal(err)
	}
	// The operator revoked nesting while this VM was down; the backup boot must
	// not hand it back.
	if err := database.SetNestingAllowed(false); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "picoclaw")
	if err := os.WriteFile(binary, []byte("agent"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SVKEXE_AGENT_BINARY", binary)

	rt := &recreateRuntime{}
	s := &Server{db: database, runtime: rt}
	run(t, s, user, "recreate dev --force")

	first := -1
	for i, step := range rt.steps {
		if step == "start" {
			first = i
			break
		}
	}
	if first < 0 {
		t.Fatalf("recreate never started the VM: %v", rt.steps)
	}
	var configured bool
	for _, step := range rt.steps[:first] {
		if step == "nesting" {
			configured = true
		}
	}
	if !configured {
		t.Errorf("the backup boot ran without applying the nesting setting: %v", rt.steps)
	}

	c, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if c.NestingApplied {
		t.Error("the rebuilt VM was recorded as booting with nesting the deployment forbids")
	}
	if !c.Nesting {
		t.Error("the ban overwrote the owner's stored wish")
	}
}
