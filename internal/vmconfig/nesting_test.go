package vmconfig

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
)

// fakeRuntime records what the gateway wrote onto each instance, which is what
// a start would put in effect.
type fakeRuntime struct {
	runtime.ContainerRuntime
	nesting map[string]bool
	fail    error
	calls   int
}

func newFakeRuntime() *fakeRuntime { return &fakeRuntime{nesting: map[string]bool{}} }

func (f *fakeRuntime) SetNesting(_ context.Context, id string, enabled bool) error {
	f.calls++
	if f.fail != nil {
		return f.fail
	}
	f.nesting[id] = enabled
	return nil
}

func fixture(t *testing.T, status string, wants bool) (*db.DB, *db.Container) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if _, err := database.EnsureUser("owner", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	c := &db.Container{ID: "vm", Name: "box", OwnerID: "owner", IncusName: "incus-box", Status: status, Nesting: wants}
	if err := database.CreateContainer(c); err != nil {
		t.Fatal(err)
	}
	return database, c
}

// Changing the setting on a running VM must reach the runtime immediately — so
// that a start from anywhere picks it up — while leaving the card's "restart
// owed" answer intact.
func TestApplyNestingLeavesARunningVMOwingARestart(t *testing.T) {
	database, c := fixture(t, "running", true)
	rt := newFakeRuntime()

	if err := ApplyNesting(t.Context(), rt, database, c); err != nil {
		t.Fatal(err)
	}
	if !rt.nesting["incus-box"] {
		t.Fatal("the setting never reached the runtime")
	}
	reloaded, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.NestingApplied {
		t.Fatal("a running VM was recorded as already booted with the new setting")
	}
}

// A stopped VM owes nothing: its next start already picks the value up, so
// recording it right away is what keeps the card from asking for a restart the
// owner does not need.
func TestApplyNestingRecordsAStoppedVMImmediately(t *testing.T) {
	database, c := fixture(t, "stopped", true)
	rt := newFakeRuntime()

	if err := ApplyNesting(t.Context(), rt, database, c); err != nil {
		t.Fatal(err)
	}
	reloaded, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.NestingApplied {
		t.Fatal("a stopped VM still owes a restart it cannot need")
	}
}

// The deployment-wide ban has to win at the point the value is written, not
// only where it is displayed: otherwise a VM whose owner asked for nesting
// would boot with it anyway.
func TestTheCeilingIsEnforcedWhereTheValueIsWritten(t *testing.T) {
	database, c := fixture(t, "stopped", true)
	if err := database.SetNestingAllowed(false); err != nil {
		t.Fatal(err)
	}
	rt := newFakeRuntime()

	if err := PrepareStart(t.Context(), rt, database, c); err != nil {
		t.Fatal(err)
	}
	if rt.nesting["incus-box"] {
		t.Fatal("a banned deployment still gave the VM nesting")
	}
	if c.NestingApplied {
		t.Fatal("the VM was recorded as booting with nesting it was denied")
	}
}

// A start is the only moment the setting can become real, so the record must
// follow the start rather than the wish.
func TestPrepareStartRecordsWhatTheVMIsAboutToBootWith(t *testing.T) {
	database, c := fixture(t, "stopped", true)
	rt := newFakeRuntime()

	if err := PrepareStart(t.Context(), rt, database, c); err != nil {
		t.Fatal(err)
	}
	if !rt.nesting["incus-box"] || !c.NestingApplied {
		t.Fatalf("runtime=%v applied=%v", rt.nesting["incus-box"], c.NestingApplied)
	}
	reloaded, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.NestingApplied {
		t.Fatal("the applied setting was not persisted")
	}
}

// A runtime that cannot be written to must not leave the database claiming the
// VM booted with a setting it never received.
func TestAFailedWriteIsNotRecordedAsApplied(t *testing.T) {
	database, c := fixture(t, "stopped", true)
	rt := newFakeRuntime()
	rt.fail = errors.New("incus is down")

	if err := PrepareStart(t.Context(), rt, database, c); err == nil {
		t.Fatal("a failed write reported success")
	}
	reloaded, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.NestingApplied {
		t.Fatal("a setting that never reached the runtime was recorded as applied")
	}
}

// The gateway runs without a container runtime in some deployments and in most
// tests; that must be a no-op rather than a panic on every start.
func TestMissingDependenciesAreANoOp(t *testing.T) {
	database, c := fixture(t, "stopped", true)
	if err := ApplyNesting(t.Context(), nil, database, c); err != nil {
		t.Fatal(err)
	}
	if err := PrepareStart(t.Context(), nil, database, c); err != nil {
		t.Fatal(err)
	}
	if err := ApplyNesting(t.Context(), newFakeRuntime(), nil, c); err != nil {
		t.Fatal(err)
	}
}

// A ceiling that cannot be read must deny rather than grant, and — crucially —
// must still be written. Returning early would leave the instance on whatever
// it last booted with, which is exactly the value an operator may have just
// revoked, and every caller starts the VM regardless of the error.
func TestAnUnreadablePolicyDeniesAndIsStillWritten(t *testing.T) {
	database, c := fixture(t, "stopped", true)
	rt := newFakeRuntime()
	// Seed the instance with the capability, so a skipped write would leave it
	// enabled and the assertion below would catch it.
	if err := PrepareStart(t.Context(), rt, database, c); err != nil {
		t.Fatal(err)
	}
	if !rt.nesting["incus-box"] {
		t.Fatal("the fixture did not start out with nesting enabled")
	}

	database.Close()

	err := PrepareStart(t.Context(), rt, database, c)
	if err == nil {
		t.Fatal("an unreadable policy was reported as a decision")
	}
	if rt.nesting["incus-box"] {
		t.Fatal("a VM booted with nesting the platform could not confirm it still allows")
	}
}

// The same rule on the settings-change path: the write happens, the error still
// travels back.
func TestApplyNestingDeniesOnAnUnreadablePolicy(t *testing.T) {
	database, c := fixture(t, "running", true)
	rt := newFakeRuntime()
	if err := ApplyNesting(t.Context(), rt, database, c); err != nil {
		t.Fatal(err)
	}
	if !rt.nesting["incus-box"] {
		t.Fatal("the fixture did not start out with nesting enabled")
	}

	database.Close()

	if err := ApplyNesting(t.Context(), rt, database, c); err == nil {
		t.Fatal("an unreadable policy was reported as a decision")
	}
	if rt.nesting["incus-box"] {
		t.Fatal("the instance kept a capability the platform could not confirm")
	}
}
