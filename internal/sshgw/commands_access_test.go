package sshgw

import (
	"fmt"
	"github.com/skrashevich/svkexe/internal/db"
	"strings"
	"testing"
)

func TestGuestCommandsAndDirectLogin(t *testing.T) {
	s, d := newTestServer(t, nil)
	owner := newTestUser(t, d, "owner", "owner@test.example", "user")
	guest := newTestUser(t, d, "guest", "guest@test.example", db.GuestRole)
	for _, id := range []string{"shared", "private"} {
		if err := d.CreateContainer(&db.Container{ID: id, Name: id, OwnerID: owner.ID, IncusName: "incus-" + id}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Exec(`INSERT INTO container_access(container_id,user_id) VALUES('shared','guest')`); err != nil {
		t.Fatal(err)
	}
	if s.accessibleVM(guest, "incus-shared") == nil || s.accessibleVM(guest, "private") != nil {
		t.Fatal("direct login isolation")
	}
	output, err := run(t, s, guest, "ls --json")
	if err != nil || !strings.Contains(output, "shared") || strings.Contains(output, "private") {
		t.Fatalf("ls: %s %v", output, err)
	}
	for _, line := range []string{"new hello", "rm shared --force", "start shared", "stop shared", "restart shared", "share shared", "rename shared changed", "recreate shared --force"} {
		if _, err := run(t, s, guest, line); err == nil {
			t.Errorf("guest could run %s", line)
		}
	}
	if err := d.RevokeContainerAccess(owner.ID, "shared", guest.ID); err != nil {
		t.Fatal(err)
	}
	if s.accessibleVM(guest, "incus-shared") != nil {
		t.Fatal("revoked direct login")
	}
	if _, err := run(t, s, guest, "stat shared"); err == nil {
		t.Fatal("revoked stat")
	}
}

func TestInaccessibleVMCommandsHaveHelpfulErrors(t *testing.T) {
	s, database := newTestServer(t, nil)
	owner := newTestUser(t, database, "owner", "owner@example.test", "user")
	guest := newTestUser(t, database, "guest", "guest@example.test", db.GuestRole)
	if err := database.CreateContainer(&db.Container{ID: "vm", Name: "encounter", OwnerID: owner.ID, IncusName: "incus-encounter"}); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"stat", "ssh"} {
		for _, name := range []string{"encounter", "nonexistent"} {
			t.Run(command+"_"+name, func(t *testing.T) {
				_, err := run(t, s, guest, command+" "+name)
				want := fmt.Sprintf("VM %q not found or access denied", name)
				if err == nil || err.Error() != want {
					t.Fatalf("got %v, want %q", err, want)
				}
			})
		}
	}
}
