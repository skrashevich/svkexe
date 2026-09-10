package sshgw

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gssh "github.com/gliderlabs/ssh"
	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
)

type testSession struct {
	gssh.Session
	output bytes.Buffer
}

func (s *testSession) Write(p []byte) (int, error) { return s.output.Write(p) }

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
			sess := &testSession{}
			s.cmdRecreate(t.Context(), sess, user, []string{"dev"})
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
				t.Fatalf("bad restore/start order or status %s: %s\n%s", c.Status, steps, sess.output.String())
			}
		})
	}
}
