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
func DeliverInitialTaskByID(ctx context.Context, rt runtime.ContainerRuntime, database *db.DB, id string, llmCfg *LLMProxyConfig) {
	if database == nil {
		return
	}
	c, err := database.GetContainerByID(id)
	if err != nil {
		log.Printf("picoclaw: load VM %s for task delivery: %v", id, err)
		return
	}
	if err := DeliverInitialTask(ctx, rt, database, c, llmCfg); err != nil {
		log.Printf("picoclaw: %v", err)
	}
}

// DeliverInitialTask hands the VM's queued task to the agent exactly once.
// It is a no-op unless the task is still pending, so restarts and gateway
// upgrades never re-run a task the agent already accepted, and a failed task
// waits for an explicit retry rather than firing on an unrelated restart.
func DeliverInitialTask(ctx context.Context, rt runtime.ContainerRuntime, database *db.DB, c *db.Container, llmCfg *LLMProxyConfig) error {
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

	// Delivery takes the same lock as setup and key refresh. Without it, saving
	// an LLM key mid-delivery lets seedProviderModels delete and reseed the
	// owner's models between the moment this reads the VM's model list and the
	// moment it posts one — so the agent is handed a model_id its database no
	// longer has — and the restart that follows can drop the POST outright.
	//
	// Giving up on the wait is recorded as a failure rather than left pending,
	// even though nothing was attempted. Delivery only ever fires on create,
	// start and an explicit retry, and every one of those routes checks for
	// TaskPending — so a task left pending here is never picked up again and the
	// dashboard shows no Retry button, because that button is rendered for
	// failed tasks. Failed is the state the owner can actually act on.
	lock, _ := setupLocks.LoadOrStore(c.IncusName, make(chan struct{}, 1))
	gate := lock.(chan struct{})
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return fail("the VM was busy being set up; retry the task", ctx.Err())
	}

	// The owner's choice and the operator's ordering reach the task the same
	// way they reach the VM's own default. Reading them here is what keeps the
	// two answers identical: a task that quietly ran on a different model would
	// spend a different key than the one the dashboard names.
	chosen, err := database.UserDefaultModel(c.OwnerID)
	if err != nil {
		return fail("could not read the account's default model", err)
	}
	own, err := database.OwnerModelIDs(c.OwnerID)
	if err != nil {
		return fail("could not list the account's own models", err)
	}
	model, err := resolveTaskModel(ctx, rt, c.IncusName, own, gatewayModelIDs(llmCfg), chosen)
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

// resolveTaskModel picks the model the task will run on, using the same rule and
// the same inputs that decide what the VM itself opens on. Two rules here is how
// a VM ends up showing one model in its UI while spending a different key in the
// background — so own and platform are passed in rather than guessed from the
// VM's list, which is alphabetical and carries neither ordering.
func resolveTaskModel(ctx context.Context, rt runtime.ContainerRuntime, incusName string, own, platform []string, chosen string) (string, error) {
	available, err := listGuestModels(ctx, rt, incusName)
	if err != nil {
		return "", err
	}
	return desiredModel(available, own, platform, chosen, readConfiguredModel(ctx, rt, incusName)), nil
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
