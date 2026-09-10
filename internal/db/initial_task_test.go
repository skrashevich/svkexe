package db

import (
	"strings"
	"testing"
)

func newTaskDB(t *testing.T) *DB {
	t.Helper()
	database := openTestDB(t)
	if _, err := database.EnsureUser("owner", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	return database
}

func TestInitialTaskStoredOnCreate(t *testing.T) {
	database := newTaskDB(t)
	if err := database.CreateContainer(&Container{
		ID: "vm", Name: "box", OwnerID: "owner", IncusName: "box", Status: "creating",
		InitialTask: "install nginx and serve a hello page",
	}); err != nil {
		t.Fatal(err)
	}
	c, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if c.InitialTask != "install nginx and serve a hello page" {
		t.Fatalf("task=%q", c.InitialTask)
	}
	// A task that was asked for must start out waiting for delivery.
	if c.InitialTaskState != TaskPending {
		t.Fatalf("state=%q, want %q", c.InitialTaskState, TaskPending)
	}
}

func TestNoInitialTaskLeavesStateEmpty(t *testing.T) {
	database := newTaskDB(t)
	if err := database.CreateContainer(&Container{
		ID: "vm", Name: "box", OwnerID: "owner", IncusName: "box", Status: "creating",
	}); err != nil {
		t.Fatal(err)
	}
	c, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if c.InitialTaskState != "" {
		t.Fatalf("state=%q, want empty", c.InitialTaskState)
	}
}

func TestCreateRejectsOversizedTask(t *testing.T) {
	database := newTaskDB(t)
	err := database.CreateContainer(&Container{
		ID: "vm", Name: "box", OwnerID: "owner", IncusName: "box", Status: "creating",
		InitialTask: strings.Repeat("x", MaxInitialTaskLen+1),
	})
	if err == nil {
		t.Fatal("oversized task accepted")
	}
}

func TestInitialTaskStateTransitions(t *testing.T) {
	database := newTaskDB(t)
	if err := database.CreateContainer(&Container{
		ID: "vm", Name: "box", OwnerID: "owner", IncusName: "box", Status: "running",
		InitialTask: "do the thing",
	}); err != nil {
		t.Fatal(err)
	}

	if err := database.SetInitialTaskState("vm", TaskFailed, "no model configured"); err != nil {
		t.Fatal(err)
	}
	c, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if c.InitialTaskState != TaskFailed || c.InitialTaskError != "no model configured" {
		t.Fatalf("state=%q err=%q", c.InitialTaskState, c.InitialTaskError)
	}
	// The task text survives a failure so it can be retried verbatim.
	if c.InitialTask != "do the thing" {
		t.Fatalf("task lost: %q", c.InitialTask)
	}

	// Success clears the stale error.
	if err := database.SetInitialTaskState("vm", TaskSent, ""); err != nil {
		t.Fatal(err)
	}
	c, err = database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if c.InitialTaskState != TaskSent || c.InitialTaskError != "" {
		t.Fatalf("state=%q err=%q", c.InitialTaskState, c.InitialTaskError)
	}
}

// Retry is the only way a failed task runs again, so it must not resurrect a
// task that already reached the agent.
func TestRetryOnlyFromFailed(t *testing.T) {
	database := newTaskDB(t)
	if err := database.CreateContainer(&Container{
		ID: "vm", Name: "box", OwnerID: "owner", IncusName: "box", Status: "running",
		InitialTask: "do the thing",
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.SetInitialTaskState("vm", TaskSent, ""); err != nil {
		t.Fatal(err)
	}
	if err := database.RetryInitialTask("vm"); err == nil {
		t.Fatal("a delivered task was queued again")
	}

	if err := database.SetInitialTaskState("vm", TaskFailed, "agent unreachable"); err != nil {
		t.Fatal(err)
	}
	if err := database.RetryInitialTask("vm"); err != nil {
		t.Fatal(err)
	}
	c, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if c.InitialTaskState != TaskPending || c.InitialTaskError != "" {
		t.Fatalf("state=%q err=%q", c.InitialTaskState, c.InitialTaskError)
	}
}

// A task delivered by a gateway that did not yet record conversations still
// has to be polled, otherwise it reports "handed to the agent" for good.
func TestListContainersWithTaskInProgressIncludesUnrecordedConversation(t *testing.T) {
	database := newTaskDB(t)
	if err := database.CreateContainer(&Container{
		ID: "vm", Name: "box", OwnerID: "owner", IncusName: "box", Status: "running",
		InitialTask: "do the thing",
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.SetInitialTaskState("vm", TaskSent, ""); err != nil {
		t.Fatal(err)
	}

	pending, err := database.ListContainersWithTaskInProgress()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != "vm" {
		t.Fatalf("got %d VMs to poll, want the one with the unrecorded conversation", len(pending))
	}

	// Once the conversation is known it is neither overwritten by a later
	// lookup nor reported as recorded, so no caller polls one the VM never
	// agreed to.
	if err := database.SetInitialTaskConversation("vm", "cFOUND"); err != nil {
		t.Fatal(err)
	}
	if err := database.SetInitialTaskConversation("vm", "cOTHER"); err == nil {
		t.Fatal("a known conversation was silently replaced")
	}
	c, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if c.InitialTaskConversation != "cFOUND" {
		t.Fatalf("conversation=%q, want the first one recorded", c.InitialTaskConversation)
	}
	if c.InitialTaskState != TaskSent {
		t.Fatalf("state=%q, recording a conversation must not change it", c.InitialTaskState)
	}

	// A stopped VM cannot answer, so it is not polled until it starts again.
	if err := database.UpdateContainerStatus("vm", "stopped", ""); err != nil {
		t.Fatal(err)
	}
	pending, err = database.ListContainersWithTaskInProgress()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("got %d VMs to poll, want none while the VM is stopped", len(pending))
	}
}

func TestRetryWithoutTaskFails(t *testing.T) {
	database := newTaskDB(t)
	if err := database.CreateContainer(&Container{
		ID: "vm", Name: "box", OwnerID: "owner", IncusName: "box", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.RetryInitialTask("vm"); err == nil {
		t.Fatal("retried a VM that never had a task")
	}
}

func TestInitialTaskMigrationPreservesRows(t *testing.T) {
	database := newTaskDB(t)
	if err := database.CreateContainer(&Container{
		ID: "vm", Name: "box", OwnerID: "owner", IncusName: "box", Status: "running",
		InitialTask: "do the thing",
	}); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"initial_task", "initial_task_state", "initial_task_error"} {
		if _, err := database.Exec("ALTER TABLE containers DROP COLUMN " + column); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := database.migrate(); err != nil {
			t.Fatal(err)
		}
	}
	c, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "box" {
		t.Fatalf("migration lost the row: %+v", c)
	}
	// An upgraded VM has no pending task, so nothing may fire on its next start.
	if c.InitialTaskState != "" {
		t.Fatalf("state=%q, want empty", c.InitialTaskState)
	}
}
