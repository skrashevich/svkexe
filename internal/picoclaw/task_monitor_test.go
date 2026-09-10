package picoclaw

import (
	"context"
	"strings"
	"testing"

	"github.com/skrashevich/svkexe/internal/db"
)

// deliveredFixture returns a VM whose task the agent already accepted.
func deliveredFixture(t *testing.T) (*db.DB, *guestRuntime, *db.Container) {
	t.Helper()
	database, guest, c := newTaskFixture(t, "install nginx")
	if err := DeliverInitialTask(context.Background(), guest, database, c); err != nil {
		t.Fatal(err)
	}
	delivered, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if delivered.InitialTaskConversation != "cTASK01" {
		t.Fatalf("conversation=%q, want the one the agent opened", delivered.InitialTaskConversation)
	}
	guest.commands = nil
	return database, guest, delivered
}

func TestRefreshTaskStateReportsProgress(t *testing.T) {
	cases := []struct {
		name       string
		progress   string
		agentError string
		wantState  string
		wantReason string
	}{
		{"agent still working", "1|user", "", db.TaskWorking, ""},
		{"agent answered", "0|agent", "", db.TaskDone, ""},
		{"agent failed", "0|error", "the model refused", db.TaskFailed, "the model refused"},
		{"agent failure without text", "0|error", "", db.TaskFailed, "the agent ended the task with an error"},
		{"turn cut short", "0|tool", "", db.TaskFailed, "restarted mid-task"},
		{"conversation gone", "-1|", "", db.TaskFailed, "no longer in the VM"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			database, guest, c := deliveredFixture(t)
			guest.progress = tc.progress
			guest.agentError = tc.agentError

			if err := RefreshTaskState(context.Background(), guest, database, c); err != nil {
				t.Fatal(err)
			}
			updated, err := database.GetContainerByID("vm")
			if err != nil {
				t.Fatal(err)
			}
			if updated.InitialTaskState != tc.wantState {
				t.Fatalf("state=%q, want %q", updated.InitialTaskState, tc.wantState)
			}
			if !strings.Contains(updated.InitialTaskError, tc.wantReason) {
				t.Fatalf("reason=%q, want it to mention %q", updated.InitialTaskError, tc.wantReason)
			}
		})
	}
}

// A VM that cannot answer right now has not failed its task: the recorded
// state has to survive until the VM can be reached again.
func TestRefreshTaskStateKeepsStateWhenVMIsUnreachable(t *testing.T) {
	database, guest, c := deliveredFixture(t)
	guest.fail = "agent_working"

	if err := RefreshTaskState(context.Background(), guest, database, c); err == nil {
		t.Fatal("unreachable VM reported as a verdict")
	}
	updated, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if updated.InitialTaskState != db.TaskSent {
		t.Fatalf("state=%q, want it left at %q", updated.InitialTaskState, db.TaskSent)
	}
}

// The conversation ID is interpolated into SQL that runs inside the VM.
func TestRefreshTaskStateRefusesUnsafeConversationID(t *testing.T) {
	database, guest, c := deliveredFixture(t)
	c.InitialTaskConversation = "x'; DROP TABLE messages; --"

	if err := RefreshTaskState(context.Background(), guest, database, c); err == nil {
		t.Fatal("unsafe conversation ID accepted")
	}
	if len(guest.commands) != 0 {
		t.Fatalf("ran a query built from an unsafe ID: %v", guest.commands)
	}
}

// Finished tasks must stop being polled, otherwise every VM ever created keeps
// costing a round trip per tick.
func TestRefreshTaskStatesOnlyPollsUnfinishedTasks(t *testing.T) {
	database, guest, c := deliveredFixture(t)
	guest.progress = "0|agent"

	RefreshTaskStates(context.Background(), database, guest)
	done, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if done.InitialTaskState != db.TaskDone {
		t.Fatalf("state=%q, want %q", done.InitialTaskState, db.TaskDone)
	}

	guest.commands = nil
	RefreshTaskStates(context.Background(), database, guest)
	if len(guest.commands) != 0 {
		t.Fatalf("kept polling a finished task: %v", guest.commands)
	}
	_ = c
}

// The agent has to name the conversation, otherwise progress could never be
// reported and the owner would watch a task that says nothing forever.
func TestDeliverInitialTaskFailsWithoutConversationID(t *testing.T) {
	database, guest, c := newTaskFixture(t, "do the thing")
	guest.newConversation = `{"status":"accepted"}`

	if err := DeliverInitialTask(context.Background(), guest, database, c); err == nil {
		t.Fatal("delivery without a conversation reported as success")
	}
	updated, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if updated.InitialTaskState != db.TaskFailed {
		t.Fatalf("state=%q, want %q", updated.InitialTaskState, db.TaskFailed)
	}
}
