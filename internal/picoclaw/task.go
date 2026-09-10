package picoclaw

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
)

// taskFilePath holds the request body while curl posts it. The task is
// user-supplied text, so it travels as a file instead of a shell argument.
const taskFilePath = ConfigDir + "/initial-task.json"

// taskWorkDir is where the agent starts working on the task.
const taskWorkDir = "/home/" + ContainerUser

const taskDeliveryTimeout = 60 * time.Second

// DeliverInitialTaskByID loads the VM and delivers its queued task. Failures
// are recorded on the VM and surfaced in the dashboard, so callers treat the
// VM as usable either way rather than failing the create or start they were
// asked for.
func DeliverInitialTaskByID(ctx context.Context, rt runtime.ContainerRuntime, database *db.DB, id string) {
	if database == nil {
		return
	}
	c, err := database.GetContainerByID(id)
	if err != nil {
		log.Printf("picoclaw: load VM %s for task delivery: %v", id, err)
		return
	}
	if err := DeliverInitialTask(ctx, rt, database, c); err != nil {
		log.Printf("picoclaw: %v", err)
	}
}

// DeliverInitialTask hands the VM's queued task to the agent exactly once.
// It is a no-op unless the task is still pending, so restarts and gateway
// upgrades never re-run a task the agent already accepted, and a failed task
// waits for an explicit retry rather than firing on an unrelated restart.
func DeliverInitialTask(ctx context.Context, rt runtime.ContainerRuntime, database *db.DB, c *db.Container) error {
	if c.InitialTask == "" || c.InitialTaskState != db.TaskPending {
		return nil
	}
	// Without a runtime there is no VM to talk to. Leave the task pending so a
	// later start delivers it, rather than burning the single attempt.
	if rt == nil || database == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, taskDeliveryTimeout)
	defer cancel()

	fail := func(reason string, err error) error {
		if stateErr := database.SetInitialTaskState(c.ID, db.TaskFailed, reason); stateErr != nil {
			log.Printf("picoclaw: record task failure for %s: %v", c.IncusName, stateErr)
		}
		return fmt.Errorf("deliver initial task to %s: %s: %w", c.IncusName, reason, err)
	}

	model, err := resolveTaskModel(ctx, rt, c.IncusName)
	if err != nil {
		return fail("could not list the models available to the agent", err)
	}
	if model == "" {
		return fail("no model is configured for this VM; add an LLM key and retry the task", fmt.Errorf("no models"))
	}

	body, err := json.Marshal(map[string]string{
		"message": c.InitialTask,
		"model":   model,
		"cwd":     taskWorkDir,
	})
	if err != nil {
		return fail("could not encode the task", err)
	}
	if err := writeGuestFile(ctx, rt, c.IncusName, taskFilePath, body); err != nil {
		return fail("could not stage the task inside the VM", err)
	}

	// Only fixed strings and the owner ID reach the shell; the task itself is
	// read from the staged file.
	post := fmt.Sprintf(
		"curl --fail --silent --show-error --max-time 30 -H %q -H 'Content-Type: application/json' --data @%s http://127.0.0.1:%d/api/conversations/new; result=$?; rm -f %s; exit $result",
		RequireHeader+": "+c.OwnerID, taskFilePath, Port, taskFilePath)
	out, err := rt.Exec(ctx, c.IncusName, []string{"sh", "-c", post})
	if err != nil {
		return fail("the agent did not accept the task", err)
	}

	// Without the conversation the task still runs, but its progress can never
	// be reported, so a missing ID is a delivery failure the owner can retry.
	conversationID, err := parseConversationID(out)
	if err != nil {
		return fail("the agent did not name the conversation it opened", err)
	}

	if err := database.SetInitialTaskDelivered(c.ID, conversationID); err != nil {
		return fmt.Errorf("record task delivery for %s: %w", c.IncusName, err)
	}
	log.Printf("picoclaw: initial task delivered to %s as conversation %s using model %s", c.IncusName, conversationID, model)
	return nil
}

// parseConversationID reads the conversation the agent opened for the task out
// of its /api/conversations/new reply.
func parseConversationID(out []byte) (string, error) {
	var reply struct {
		ConversationID string `json:"conversation_id"`
	}
	if err := json.Unmarshal(out, &reply); err != nil {
		return "", fmt.Errorf("decode agent reply %q: %w", strings.TrimSpace(string(out)), err)
	}
	if !validConversationID(reply.ConversationID) {
		return "", fmt.Errorf("agent returned conversation ID %q", reply.ConversationID)
	}
	return reply.ConversationID, nil
}

// resolveTaskModel picks the model the task will run on.
func resolveTaskModel(ctx context.Context, rt runtime.ContainerRuntime, incusName string) (string, error) {
	out, err := rt.Exec(ctx, incusName, []string{"sqlite3", DBPath, "SELECT model_id FROM models ORDER BY model_id;"})
	if err != nil {
		return "", err
	}
	var available []string
	for _, line := range strings.Split(string(out), "\n") {
		if id := strings.TrimSpace(line); id != "" {
			available = append(available, id)
		}
	}
	// The same rule that decides what the VM opens on decides what its first
	// task runs on. Two rules here is how a VM ends up showing one model in its
	// UI while spending another owner's quota in the background.
	return desiredModel(available, ownModels(available), "", readConfiguredModel(ctx, rt, incusName)), nil
}

// readConfiguredModel returns the VM's default model, or "" when it cannot be
// read — an unreadable config is not a reason to refuse the task.
func readConfiguredModel(ctx context.Context, rt runtime.ContainerRuntime, incusName string) string {
	out, err := rt.Exec(ctx, incusName, []string{"cat", ConfigFilePath})
	if err != nil {
		return ""
	}
	var cfg struct {
		DefaultModel string `json:"default_model"`
	}
	if err := json.Unmarshal(out, &cfg); err != nil {
		return ""
	}
	return cfg.DefaultModel
}

func firstWithPrefix(ids []string, prefix string) string {
	for _, id := range ids {
		if strings.HasPrefix(id, prefix) {
			return id
		}
	}
	return ""
}
