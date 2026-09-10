package picoclaw

import (
	"context"
	"encoding/hex"
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

// stuckFixture returns the shape gateways older than the conversation column
// left behind: a task the agent accepted, with no conversation recorded.
func stuckFixture(t *testing.T, task string) (*db.DB, *guestRuntime, *db.Container) {
	t.Helper()
	database, guest, _ := newTaskFixture(t, task)
	if err := database.SetInitialTaskState("vm", db.TaskSent, ""); err != nil {
		t.Fatal(err)
	}
	c, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if c.InitialTaskConversation != "" {
		t.Fatalf("conversation=%q, want the stuck shape to have none", c.InitialTaskConversation)
	}
	return database, guest, c
}

// Such a task must be traced back to its conversation, otherwise the card keeps
// saying the agent is starting on a VM that finished the work long ago.
func TestRefreshTaskStateAdoptsAnUnrecordedConversation(t *testing.T) {
	database, guest, c := stuckFixture(t, "install nginx")
	guest.taskLookup = "cOLD01"
	guest.progress = "0|agent"

	if err := RefreshTaskState(context.Background(), guest, database, c); err != nil {
		t.Fatal(err)
	}
	updated, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if updated.InitialTaskConversation != "cOLD01" {
		t.Fatalf("conversation=%q, want the one found in the VM", updated.InitialTaskConversation)
	}
	// The same round must report what that conversation says, not wait for the
	// next one.
	if updated.InitialTaskState != db.TaskDone {
		t.Fatalf("state=%q, want %q", updated.InitialTaskState, db.TaskDone)
	}
}

// A task no conversation matches can never report progress again, so it has to
// become retryable instead of spinning forever.
func TestRefreshTaskStateGivesUpWhenNoConversationMatches(t *testing.T) {
	database, guest, c := stuckFixture(t, "install nginx")
	guest.taskLookup = ""

	if err := RefreshTaskState(context.Background(), guest, database, c); err != nil {
		t.Fatal(err)
	}
	updated, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if updated.InitialTaskState != db.TaskFailed {
		t.Fatalf("state=%q, want %q", updated.InitialTaskState, db.TaskFailed)
	}
	if !strings.Contains(updated.InitialTaskError, "retry") {
		t.Fatalf("reason=%q, want it to point at a retry", updated.InitialTaskError)
	}
	if err := database.RetryInitialTask("vm"); err != nil {
		t.Fatalf("the owner cannot retry the task: %v", err)
	}
}

// The task is user-supplied text, so looking its conversation up must not put
// it in front of the guest shell or into the query as text.
func TestConversationLookupKeepsTaskTextOutOfShell(t *testing.T) {
	task := "install nginx; run $(touch /tmp/pwned) and 'quote\" it\nsecond line"
	database, guest, c := stuckFixture(t, task)
	guest.taskLookup = "cOLD01"
	guest.progress = "0|agent"

	if err := RefreshTaskState(context.Background(), guest, database, c); err != nil {
		t.Fatal(err)
	}
	script := strings.Join(guest.commands, "\n")
	if strings.Contains(script, "touch /tmp/pwned") || strings.Contains(script, "second line") {
		t.Fatalf("task text entered guest commands: %s", script)
	}
	// It travels as hex, and hex is all that reaches the VM.
	if !strings.Contains(script, hex.EncodeToString([]byte(task))) {
		t.Fatalf("task was not sent as a hex literal: %s", script)
	}
	if strings.Contains(script, "sh -c") {
		t.Errorf("the lookup went through a shell: %s", script)
	}
}

// Reading the agent's database must not create one: sqlite3 leaves an empty
// file behind for a failing read, and that file would defeat the shelley.db
// migration a VM that has not been set up yet still needs.
func TestAgentDatabaseIsOnlyEverReadOnly(t *testing.T) {
	database, guest, c := stuckFixture(t, "install nginx")
	guest.taskLookup = "cOLD01"
	guest.progress = "0|agent"

	if err := RefreshTaskState(context.Background(), guest, database, c); err != nil {
		t.Fatal(err)
	}
	var queries int
	for _, cmd := range guest.commands {
		if !strings.HasPrefix(cmd, "sqlite3 ") {
			continue
		}
		queries++
		if !strings.HasPrefix(cmd, "sqlite3 -readonly "+DBPath+" ") {
			t.Errorf("query may write to the agent database: %s", cmd)
		}
	}
	// At least the conversation lookup and the progress query.
	if queries < 2 {
		t.Fatalf("ran %d queries against the agent database, want the lookup and the poll", queries)
	}
}

// The recovered ID is interpolated into SQL that runs inside the VM.
func TestRefreshTaskStateRefusesUnsafeRecoveredConversationID(t *testing.T) {
	database, guest, c := stuckFixture(t, "install nginx")
	guest.taskLookup = "x'; DROP TABLE messages; --"

	if err := RefreshTaskState(context.Background(), guest, database, c); err == nil {
		t.Fatal("unsafe conversation ID accepted")
	}
	updated, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if updated.InitialTaskConversation != "" {
		t.Fatalf("stored an unsafe conversation ID: %q", updated.InitialTaskConversation)
	}
	if updated.InitialTaskState != db.TaskSent {
		t.Fatalf("state=%q, want it left at %q", updated.InitialTaskState, db.TaskSent)
	}
}

// A stopped VM cannot be asked anything, but its task must not be written off
// either: the next start reads back how it actually ended.
func TestRefreshTaskStatesLeavesAStoppedVMForItsNextStart(t *testing.T) {
	database, guest, _ := deliveredFixture(t)
	if err := database.UpdateContainerStatus("vm", "stopped", ""); err != nil {
		t.Fatal(err)
	}
	guest.progress = "0|agent"

	RefreshTaskStates(context.Background(), database, guest)
	if len(guest.commands) != 0 {
		t.Fatalf("polled a stopped VM: %v", guest.commands)
	}
	stopped, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if stopped.InitialTaskState != db.TaskSent {
		t.Fatalf("state=%q, want it kept at %q", stopped.InitialTaskState, db.TaskSent)
	}

	if err := database.UpdateContainerStatus("vm", "running", ""); err != nil {
		t.Fatal(err)
	}
	RefreshTaskStates(context.Background(), database, guest)
	started, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if started.InitialTaskState != db.TaskDone {
		t.Fatalf("state=%q, want the restarted VM to report %q", started.InitialTaskState, db.TaskDone)
	}
}
