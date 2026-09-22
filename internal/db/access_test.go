package db

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"golang.org/x/crypto/ssh"
	"path/filepath"
	"testing"
	"time"
)

func accessTestKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(ssh.MarshalAuthorizedKey(key))
}
func TestContainerAccessIdentityAndIsolation(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, id := range []string{"owner", "other"} {
		if _, err := d.EnsureUser(id, id+"@test.example"); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"one", "two"} {
		if err := d.CreateContainer(&Container{ID: id, Name: id, OwnerID: "owner", IncusName: id}); err != nil {
			t.Fatal(err)
		}
	}
	key := accessTestKey(t)
	if err := d.GrantContainerAccess("other", "one", "guest@test.example", key); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("stranger grant: %v", err)
	}
	if err := d.GrantContainerAccess("owner", "one", "Guest@Test.Example", key); err != nil {
		t.Fatal(err)
	}
	guest, err := d.GetUserByEmail("guest@test.example")
	if err != nil {
		t.Fatal(err)
	}
	if guest.Role != GuestRole || !d.CanUseContainer("one", guest.ID) || d.CanUseContainer("two", guest.ID) {
		t.Fatal("guest isolation failed")
	}
	if err := d.GrantContainerAccess("owner", "two", "guest@test.example", accessTestKey(t)); err == nil {
		t.Fatal("inviter can replace an existing identity")
	}
	keys, err := d.ListSSHKeysByUser(guest.ID)
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys: %v %v", keys, err)
	}
	if err := d.GrantContainerAccess("owner", "two", "another@test.example", key); err == nil {
		t.Fatal("duplicate key accepted")
	}
	if _, err := d.GetUserByEmail("another@test.example"); err == nil {
		t.Fatal("failed invitation left an account")
	}
	if err := d.GrantContainerAccess("owner", "two", "guest@test.example", key); err != nil {
		t.Fatal(err)
	}
	if err := d.RevokeContainerAccess("other", "one", guest.ID); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("stranger revoke: %v", err)
	}
	if err := d.RevokeContainerAccess("owner", "one", guest.ID); err != nil {
		t.Fatal(err)
	}
	if d.CanUseContainer("one", guest.ID) || !d.CanUseContainer("two", guest.ID) {
		t.Fatal("revocation was not VM-specific")
	}
	if err := d.DeleteContainer("two"); err != nil {
		t.Fatal(err)
	}
	members, err := d.ListContainerAccess("two")
	if err != nil || len(members) != 0 {
		t.Fatal("orphaned grant")
	}
}
func TestAmbiguousAccessibleNames(t *testing.T) {
	d, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, id := range []string{"a", "b", "guest"} {
		if _, err := d.EnsureUser(id, id+"@test.example"); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"a", "b"} {
		if err := d.CreateContainer(&Container{ID: id, Name: "same", OwnerID: id, IncusName: "incus-" + id}); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(`INSERT INTO container_access(container_id,user_id) VALUES(?,'guest')`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.ResolveAccessibleContainer("same", "guest"); err == nil {
		t.Fatal("ambiguous name accepted")
	}
	if c, err := d.ResolveAccessibleContainer("incus-a", "guest"); err != nil || c.ID != "a" {
		t.Fatalf("exact name: %v %v", c, err)
	}
}

func TestRevokedAccessCancelsConnection(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, id := range []string{"owner", "guest"} {
		if _, err := d.EnsureUser(id, id+"@test.example"); err != nil {
			t.Fatal(err)
		}
	}
	c := &Container{ID: "vm", Name: "vm", IncusName: "vm", OwnerID: "owner"}
	if err := d.CreateContainer(c); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO container_access(container_id,user_id) VALUES('vm','guest')`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := d.ContainerAccessContext(t.Context(), c, "guest")
	defer cancel()
	if err := d.RevokeContainerAccess("owner", "vm", "guest"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("revoked connection remained open")
	}
}
