package picoclaw

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"slices"
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
	if _, err := rt.Exec(ctx, c.IncusName, []string{"sh", "-c", post}); err != nil {
		return fail("the agent did not accept the task", err)
	}

	if err := database.SetInitialTaskState(c.ID, db.TaskSent, ""); err != nil {
		return fmt.Errorf("record task delivery for %s: %w", c.IncusName, err)
	}
	log.Printf("picoclaw: initial task delivered to %s using model %s", c.IncusName, model)
	return nil
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
	return preferredModel(available, readConfiguredModel(ctx, rt, incusName)), nil
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

// preferredModel runs the task on one of the owner's own models whenever they
// have any: the gateway list is deployment-wide and may name models this
// account cannot reach. Their configured default is honoured when it is such a
// model; otherwise the gateway serves as the fallback.
func preferredModel(available []string, configured string) string {
	if user := firstWithPrefix(available, userModelPrefix); user != "" {
		if strings.HasPrefix(configured, userModelPrefix) && slices.Contains(available, configured) {
			return configured
		}
		return user
	}
	if configured != "" && slices.Contains(available, configured) {
		return configured
	}
	if gateway := firstWithPrefix(available, gatewayModelPrefix); gateway != "" {
		return gateway
	}
	if len(available) > 0 {
		return available[0]
	}
	return ""
}

func firstWithPrefix(ids []string, prefix string) string {
	for _, id := range ids {
		if strings.HasPrefix(id, prefix) {
			return id
		}
	}
	return ""
}
