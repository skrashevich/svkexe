package db

import (
	"path/filepath"
	"testing"
)

func nestingDB(t *testing.T) *DB {
	t.Helper()
	database, err := Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	// Containers reference their owner, so the fixtures below need one to exist.
	if _, err := database.EnsureUser("owner", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	return database
}

// A deployment that never touched the switch has to allow nesting: the whole
// point of the platform is running other people's builds, and a VM where Docker
// cannot start is the surprising answer, not the safe one.
func TestNestingIsAllowedUntilAnOperatorSaysOtherwise(t *testing.T) {
	database := nestingDB(t)

	allowed, err := database.NestingAllowed()
	if err != nil {
		t.Fatal(err)
	}
	if !allowed {
		t.Fatal("a fresh deployment forbids nested containers")
	}

	if err := database.SetNestingAllowed(false); err != nil {
		t.Fatal(err)
	}
	if allowed, err = database.NestingAllowed(); err != nil || allowed {
		t.Fatalf("allowed=%v err=%v after turning the policy off", allowed, err)
	}

	// Storing the same key twice must move the value rather than fail on the
	// primary key.
	if err := database.SetNestingAllowed(true); err != nil {
		t.Fatal(err)
	}
	if allowed, err = database.NestingAllowed(); err != nil || !allowed {
		t.Fatalf("allowed=%v err=%v after turning the policy back on", allowed, err)
	}
}

// The deployment-wide switch is a ceiling, not a default: a VM whose owner
// wants nesting must still come back "off" while the operator forbids it,
// because that is what makes the switch usable as a single lever.
func TestTheDeploymentCeilingOverridesTheOwnersWish(t *testing.T) {
	database := nestingDB(t)
	c := &Container{ID: "vm", Name: "box", OwnerID: "owner", IncusName: "box", Status: "running", Nesting: true}
	mustCreate(t, database, c)

	if err := database.AttachNestingPolicy(c); err != nil {
		t.Fatal(err)
	}
	if !c.NestingEffective() {
		t.Fatal("an owner who wants nesting does not get it on an unrestricted deployment")
	}

	if err := database.SetNestingAllowed(false); err != nil {
		t.Fatal(err)
	}
	if err := database.AttachNestingPolicy(c); err != nil {
		t.Fatal(err)
	}
	if c.NestingEffective() {
		t.Fatal("the owner's wish survived a deployment-wide ban")
	}
	// The wish itself must survive, so lifting the ban restores what the owner
	// chose instead of silently leaving every VM off.
	if !c.Nesting {
		t.Fatal("the ban overwrote the owner's stored choice")
	}
}

// The setting is read by the runtime when the container boots, so a change made
// against a running VM is a promise until it restarts. The card has to be able
// to say that.
func TestNestingPendingTracksTheRestartTheVMStillOwes(t *testing.T) {
	database := nestingDB(t)
	c := &Container{ID: "vm", Name: "box", OwnerID: "owner", IncusName: "box", Status: "running", Nesting: true}
	mustCreate(t, database, c)
	if err := database.AttachNestingPolicy(c); err != nil {
		t.Fatal(err)
	}

	// Fresh from the migration: wanted but never applied.
	if !c.NestingPending() {
		t.Fatal("a VM running without the nesting it was given does not ask for a restart")
	}

	if err := database.SetNestingApplied("vm", true); err != nil {
		t.Fatal(err)
	}
	reloaded := mustLoad(t, database, "vm")
	if err := database.AttachNestingPolicy(reloaded); err != nil {
		t.Fatal(err)
	}
	if reloaded.NestingPending() {
		t.Fatal("a VM that booted with the setting still asks for a restart")
	}

	// Turning it back off is equally pending: the container keeps nesting until
	// it boots again.
	if err := database.UpdateContainerNesting("vm", false); err != nil {
		t.Fatal(err)
	}
	reloaded = mustLoad(t, database, "vm")
	if err := database.AttachNestingPolicy(reloaded); err != nil {
		t.Fatal(err)
	}
	if !reloaded.NestingPending() {
		t.Fatal("turning nesting off does not ask for the restart that would enforce it")
	}
	if reloaded.NestingApplied != true {
		t.Fatal("saving the wish overwrote what the VM actually booted with")
	}

	// A VM that is not running owes nothing: whatever is stored takes effect the
	// moment it next boots.
	if err := database.UpdateContainerStatus("vm", "stopped", ""); err != nil {
		t.Fatal(err)
	}
	reloaded = mustLoad(t, database, "vm")
	if err := database.AttachNestingPolicy(reloaded); err != nil {
		t.Fatal(err)
	}
	if reloaded.NestingPending() {
		t.Fatal("a stopped VM asks for a restart it does not need")
	}
}

// An upgraded database must not quietly leave every existing VM unable to run
// Docker, and must not claim the capability is already live either: those VMs
// were built under a profile that disabled it.
func TestUpgradedVMsWantNestingButHaveNotBootedWithIt(t *testing.T) {
	database := nestingDB(t)
	// A row written the way an older gateway wrote it, i.e. without either of
	// the new columns.
	if _, err := database.Exec(
		`INSERT INTO containers (id, name, owner_id, incus_name, status) VALUES ('old', 'legacy', 'owner', 'legacy', 'running')`,
	); err != nil {
		t.Fatal(err)
	}
	c := mustLoad(t, database, "old")
	if !c.Nesting {
		t.Fatal("an upgraded VM was left unable to run nested containers")
	}
	if c.NestingApplied {
		t.Fatal("an upgraded VM claims it already booted with nesting")
	}
}

func mustCreate(t *testing.T, database *DB, c *Container) {
	t.Helper()
	if err := database.CreateContainer(c); err != nil {
		t.Fatal(err)
	}
}

func mustLoad(t *testing.T, database *DB, id string) *Container {
	t.Helper()
	c, err := database.GetContainerByID(id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
