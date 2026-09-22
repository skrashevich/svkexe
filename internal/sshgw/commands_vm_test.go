package sshgw

import (
	"context"
	"strings"
	"testing"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
)

// deleteRuntime records the container lifecycle calls a destructive command
// makes, so a test can tell "refused" from "did it quietly".
type deleteRuntime struct {
	runtime.ContainerRuntime
	deleted []string
	created []runtime.CreateOpts
}

func (r *deleteRuntime) Stop(context.Context, string) error  { return nil }
func (r *deleteRuntime) Start(context.Context, string) error { return nil }
func (r *deleteRuntime) Delete(_ context.Context, id string) error {
	r.deleted = append(r.deleted, id)
	return nil
}
func (r *deleteRuntime) Create(_ context.Context, opts runtime.CreateOpts) (*runtime.Container, error) {
	r.created = append(r.created, opts)
	return &runtime.Container{Name: "svkexe-u1-" + opts.Name, Status: "running"}, nil
}
func (r *deleteRuntime) Get(context.Context, string) (*runtime.Container, error) {
	return &runtime.Container{IP: "10.0.0.2"}, nil
}
func (r *deleteRuntime) SetNesting(context.Context, string, bool) error { return nil }

// A VM name is a DNS label in every address the VM answers on, and the platform
// additionally reserves the routing prefixes. The SSH shell used to apply a
// looser rule of its own, which let a tenant create a name the rest of the
// gateway treats as impossible.
func TestVMNamesFollowThePlatformRule(t *testing.T) {
	rt := &deleteRuntime{}
	s, database := newTestServer(t, rt)
	user := newTestUser(t, database, "u1", "u@example.test", "user")
	if err := database.CreateContainer(&db.Container{
		ID: "vm", Name: "dev", IncusName: "svkexe-u1-dev", OwnerID: user.ID, Status: "stopped",
	}); err != nil {
		t.Fatal(err)
	}

	refused := []string{
		"agent-victim", // would shadow the agent host of the VM called "victim"
		"3000-app",     // would shadow the explicit-port host of "app"
		"Dev",          // uppercase: hostnames are compared in lower case
		"my_vm",        // underscore is not a DNS label character
		"a",            // one character
		"my.vm",        // a dot makes it two labels
	}
	for _, name := range refused {
		t.Run("new_"+name, func(t *testing.T) {
			if _, err := run(t, s, user, "new "+name); err == nil {
				t.Fatalf("the shell accepted the VM name %q", name)
			}
			if len(rt.created) != 0 {
				t.Fatalf("the runtime was asked to build %q", name)
			}
		})
		t.Run("rename_"+name, func(t *testing.T) {
			if _, err := run(t, s, user, "rename dev "+name); err == nil {
				t.Fatalf("the shell renamed a VM to %q", name)
			}
			c, err := database.GetContainerByID("vm")
			if err != nil {
				t.Fatal(err)
			}
			if c.Name != "dev" {
				t.Fatalf("the VM was renamed to %q anyway", c.Name)
			}
		})
	}
}

// The resource flags are written by whoever scripts the command, often an
// agent, and nothing reads the numbers back before a VM reserves them.
func TestNewRefusesImplausibleResources(t *testing.T) {
	rt := &deleteRuntime{}
	s, database := newTestServer(t, rt)
	user := newTestUser(t, database, "u1", "u@example.test", "user")

	for _, line := range []string{
		"new dev --cpu=0",
		"new dev --cpu=4096",
		"new dev --memory=99999999",
		"new dev --disk=1000000",
		"new dev --cpu=4abc", // a partial number used to be read as 4
	} {
		if _, err := run(t, s, user, line); err == nil {
			t.Errorf("%q was accepted", line)
		}
	}
	if len(rt.created) != 0 {
		t.Fatalf("the runtime was asked to build %d VMs", len(rt.created))
	}
}

// "ssh dev@gateway rm foo" deletes a file inside the VM while the VM exists,
// and deletes the VM called foo once it does not. A script pinned to the old
// name must not cross that line without saying so.
func TestDestructiveCommandsNeedForceInAOneShot(t *testing.T) {
	rt := &deleteRuntime{}
	s, database := newTestServer(t, rt)
	user := newTestUser(t, database, "u1", "u@example.test", "user")
	for _, name := range []string{"one", "two", "three"} {
		if err := database.CreateContainer(&db.Container{
			ID: name, Name: name, IncusName: "svkexe-u1-" + name, OwnerID: user.ID, Status: "stopped",
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := run(t, s, user, "rm one"); err == nil {
		t.Fatal("a one-shot delete went through without --force")
	}
	if len(rt.deleted) != 0 {
		t.Fatalf("the VM was deleted anyway: %v", rt.deleted)
	}
	if _, err := database.GetContainerByID("one"); err != nil {
		t.Fatal("the VM record is gone after a refused delete")
	}

	if _, err := run(t, s, user, "rm one --force"); err != nil {
		t.Fatalf("--force did not carry the delete: %v", err)
	}
	// Typed at the prompt, a human has already said what they meant.
	if _, err := runInteractive(t, s, user, "rm two"); err != nil {
		t.Fatalf("an interactive delete was refused: %v", err)
	}
	if strings.Join(rt.deleted, ",") != "svkexe-u1-one,svkexe-u1-two" {
		t.Fatalf("the wrong VMs were deleted: %v", rt.deleted)
	}
}
