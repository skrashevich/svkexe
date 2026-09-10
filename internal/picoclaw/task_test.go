package picoclaw

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skrashevich/svkexe/internal/db"
)

func newTaskFixture(t *testing.T, task string) (*db.DB, *guestRuntime, *db.Container) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if _, err := database.EnsureUser("owner", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateContainer(&db.Container{
		ID: "vm", Name: "box", OwnerID: "owner", IncusName: "box",
		Status: "running", IPAddress: "10.0.0.2", InitialTask: task,
	}); err != nil {
		t.Fatal(err)
	}
	c, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	return database, &guestRuntime{files: map[string][]byte{}}, c
}

// The task is user-supplied text, so it must reach the agent verbatim without
// ever being interpreted by the guest shell.
func TestDeliverInitialTaskKeepsTextOutOfShell(t *testing.T) {
	task := "install nginx; run $(touch /tmp/pwned) and 'quote\" it\nsecond line"
	database, guest, c := newTaskFixture(t, task)

	if err := DeliverInitialTask(context.Background(), guest, database, c); err != nil {
		t.Fatal(err)
	}

	script := strings.Join(guest.commands, "\n")
	if strings.Contains(script, "touch /tmp/pwned") || strings.Contains(script, "second line") {
		t.Fatalf("task text entered shell code: %s", script)
	}

	body, ok := guest.files[taskFilePath]
	if !ok {
		t.Fatal("task body was never written to the guest")
	}
	var payload struct {
		Message string `json:"message"`
		Model   string `json:"model"`
		Cwd     string `json:"cwd"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Message != task {
		t.Fatalf("task altered in transit: %q", payload.Message)
	}
	if payload.Model != "svkexe-test/model" {
		t.Fatalf("model=%q", payload.Model)
	}
	if payload.Cwd == "" {
		t.Error("no working directory given to the agent")
	}

	// It must be posted as the owner, and the temporary file cleaned up.
	if !strings.Contains(script, "/api/conversations/new") {
		t.Error("conversation was never started")
	}
	if !strings.Contains(script, RequireHeader+": owner") {
		t.Errorf("task not posted as the owner: %s", script)
	}
	if !strings.Contains(script, "rm -f "+taskFilePath) {
		t.Error("task file left behind in the guest")
	}

	updated, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if updated.InitialTaskState != db.TaskSent {
		t.Fatalf("state=%q, want %q", updated.InitialTaskState, db.TaskSent)
	}
}

// A VM without any usable model cannot run the task; the reason has to reach
// the owner instead of disappearing into the log.
func TestDeliverInitialTaskWithoutModel(t *testing.T) {
	database, guest, c := newTaskFixture(t, "do the thing")
	guest.models = " \n"

	err := DeliverInitialTask(context.Background(), guest, database, c)
	if err == nil {
		t.Fatal("missing model reported as success")
	}

	updated, dbErr := database.GetContainerByID("vm")
	if dbErr != nil {
		t.Fatal(dbErr)
	}
	if updated.InitialTaskState != db.TaskFailed {
		t.Fatalf("state=%q, want %q", updated.InitialTaskState, db.TaskFailed)
	}
	if !strings.Contains(updated.InitialTaskError, "model") {
		t.Errorf("unhelpful reason: %q", updated.InitialTaskError)
	}
	// The text stays so a retry can send it once a key is configured.
	if updated.InitialTask != "do the thing" {
		t.Errorf("task lost: %q", updated.InitialTask)
	}
}

func TestDeliverInitialTaskRecordsAgentFailure(t *testing.T) {
	database, guest, c := newTaskFixture(t, "do the thing")
	guest.fail = "/api/conversations/new"

	if err := DeliverInitialTask(context.Background(), guest, database, c); err == nil {
		t.Fatal("agent failure reported as success")
	}
	updated, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if updated.InitialTaskState != db.TaskFailed || updated.InitialTaskError == "" {
		t.Fatalf("state=%q err=%q", updated.InitialTaskState, updated.InitialTaskError)
	}
}

// Restarting a VM must not re-run a task the agent already accepted.
func TestDeliverInitialTaskOnlyOnce(t *testing.T) {
	database, guest, c := newTaskFixture(t, "do the thing")
	if err := DeliverInitialTask(context.Background(), guest, database, c); err != nil {
		t.Fatal(err)
	}
	before := len(guest.commands)

	sent, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if err := DeliverInitialTask(context.Background(), guest, database, sent); err != nil {
		t.Fatal(err)
	}
	if len(guest.commands) != before {
		t.Fatalf("delivered task was sent again: %v", guest.commands[before:])
	}
}

func TestDeliverInitialTaskSkipsFailedUntilRetry(t *testing.T) {
	database, guest, c := newTaskFixture(t, "do the thing")
	if err := database.SetInitialTaskState("vm", db.TaskFailed, "earlier failure"); err != nil {
		t.Fatal(err)
	}
	failed, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if err := DeliverInitialTask(context.Background(), guest, database, failed); err != nil {
		t.Fatal(err)
	}
	if len(guest.commands) != 0 {
		t.Fatalf("a failed task retried itself: %v", guest.commands)
	}

	// After an explicit retry it goes out.
	if err := database.RetryInitialTask("vm"); err != nil {
		t.Fatal(err)
	}
	pending, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if err := DeliverInitialTask(context.Background(), guest, database, pending); err != nil {
		t.Fatal(err)
	}
	if _, ok := guest.files[taskFilePath]; !ok {
		t.Fatal("retry did not deliver the task")
	}
	_ = c
}

func TestDeliverInitialTaskNoopWithoutTask(t *testing.T) {
	database, guest, _ := newTaskFixture(t, "")
	c, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if err := DeliverInitialTask(context.Background(), guest, database, c); err != nil {
		t.Fatal(err)
	}
	if len(guest.commands) != 0 {
		t.Fatalf("ran commands for a VM with no task: %v", guest.commands)
	}
}

// The initial task is the first thing a new VM runs; sending it to a gateway
// model the account cannot reach fails the task the owner just queued.
func TestDeliverInitialTaskUsesOwnerModel(t *testing.T) {
	database, guest, c := newTaskFixture(t, "do the thing")
	guest.models = "svkexe-cohere/north-mini-code:free\nsvkexe_user:openrouter:openrouter/free\n"
	guest.files[ConfigFilePath] = []byte(`{"default_model":"svkexe-cohere/north-mini-code:free"}`)

	if err := DeliverInitialTask(context.Background(), guest, database, c); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(guest.files[taskFilePath], &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Model != "svkexe_user:openrouter:openrouter/free" {
		t.Fatalf("task sent to %q, want the owner's own model", payload.Model)
	}
}

// The task path derives the owner's models from the VM's own list, so this
// pins desiredModel through exactly the arguments resolveTaskModel gives it.
func TestTaskModelSelection(t *testing.T) {
	cases := []struct {
		name       string
		available  []string
		configured string
		want       string
	}{
		{"configured user model wins", []string{"svkexe-a", "svkexe_user:p:b", "svkexe_user:p:c"}, "svkexe_user:p:c", "svkexe_user:p:c"},
		{"user model before gateway model", []string{"svkexe_user:p:b", "svkexe-a"}, "", "svkexe_user:p:b"},
		{"gateway default gives way to the owner's key", []string{"svkexe-a", "svkexe_user:p:b"}, "svkexe-a", "svkexe_user:p:b"},
		{"configured user model gone falls back to another", []string{"svkexe-a", "svkexe_user:p:b"}, "svkexe_user:p:gone", "svkexe_user:p:b"},
		{"user model when no gateway model", []string{"svkexe_user:p:b"}, "", "svkexe_user:p:b"},
		{"gateway model when the owner has no keys", []string{"other", "svkexe-a"}, "", "svkexe-a"},
		{"configured gateway model honoured without user keys", []string{"svkexe-a", "svkexe-b"}, "svkexe-b", "svkexe-b"},
		{"configured but absent falls back", []string{"svkexe-a"}, "svkexe-gone", "svkexe-a"},
		{"nothing available", nil, "svkexe-gone", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := desiredModel(tc.available, ownModels(tc.available), "", tc.configured); got != tc.want {
				t.Errorf("desiredModel(%v, %q) = %q, want %q", tc.available, tc.configured, got, tc.want)
			}
		})
	}
}

// A caller without a runtime must leave the task queued instead of panicking
// or burning its single delivery attempt.
func TestDeliverInitialTaskWithoutRuntime(t *testing.T) {
	database, _, c := newTaskFixture(t, "do the thing")
	if err := DeliverInitialTask(context.Background(), nil, database, c); err != nil {
		t.Fatal(err)
	}
	updated, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if updated.InitialTaskState != db.TaskPending {
		t.Fatalf("state=%q, want %q", updated.InitialTaskState, db.TaskPending)
	}
}
