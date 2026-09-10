package picoclaw

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
)

// TaskPollInterval is how often a delivered task is checked for progress. The
// dashboard reloads every 5s, so this is the resolution the owner sees.
const TaskPollInterval = 15 * time.Second

// taskPollTimeout bounds one VM's progress query. A VM that cannot answer in
// time keeps its recorded state and is asked again on the next round.
const taskPollTimeout = 20 * time.Second

// maxTaskErrorLen keeps an agent error readable in the VM card.
const maxTaskErrorLen = 500

// conversationIDRE matches the IDs the agent hands out. The ID is interpolated
// into SQL that runs inside the VM, so anything else is refused rather than
// escaped.
var conversationIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func validConversationID(id string) bool {
	return conversationIDRE.MatchString(id)
}

// MonitorTasks keeps the recorded state of every delivered task in step with
// what the agent is actually doing, so the dashboard can tell "the agent is
// still working" apart from "finished" and "failed". It returns when ctx ends.
func MonitorTasks(ctx context.Context, database *db.DB, rt runtime.ContainerRuntime, interval time.Duration) {
	if database == nil || rt == nil {
		return
	}
	if interval <= 0 {
		interval = TaskPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			RefreshTaskStates(ctx, database, rt)
		}
	}
}

// RefreshTaskStates polls every VM whose task can still change state.
func RefreshTaskStates(ctx context.Context, database *db.DB, rt runtime.ContainerRuntime) {
	if database == nil || rt == nil {
		return
	}
	containers, err := database.ListContainersWithTaskInProgress()
	if err != nil {
		log.Printf("picoclaw: list VMs with a running task: %v", err)
		return
	}
	for _, c := range containers {
		if ctx.Err() != nil {
			return
		}
		if err := RefreshTaskState(ctx, rt, database, c); err != nil {
			log.Printf("picoclaw: %v", err)
		}
	}
}

// RefreshTaskState records what the agent is doing with this VM's task. A VM
// that cannot be reached keeps its current state: an unreachable agent is not
// a failed task, and the next round asks again.
func RefreshTaskState(ctx context.Context, rt runtime.ContainerRuntime, database *db.DB, c *db.Container) error {
	if c == nil || rt == nil || database == nil {
		return nil
	}
	if !db.TaskInProgress(c.InitialTaskState) {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, taskPollTimeout)
	defer cancel()

	if c.InitialTaskConversation == "" {
		if err := adoptConversation(ctx, rt, database, c); err != nil {
			return err
		}
		if c.InitialTaskConversation == "" {
			return nil
		}
	}
	if !validConversationID(c.InitialTaskConversation) {
		return fmt.Errorf("task progress for %s: refusing conversation ID %q", c.IncusName, c.InitialTaskConversation)
	}

	state, reason, err := pollTaskState(ctx, rt, c.IncusName, c.InitialTaskConversation)
	if err != nil {
		return fmt.Errorf("read task progress for %s: %w", c.IncusName, err)
	}
	if state == c.InitialTaskState && reason == c.InitialTaskError {
		return nil
	}
	if err := database.SetInitialTaskState(c.ID, state, reason); err != nil {
		return fmt.Errorf("record task progress for %s: %w", c.IncusName, err)
	}
	log.Printf("picoclaw: task on %s is now %s", c.IncusName, state)
	return nil
}

// lostConversationReason is what the owner is told when the task cannot be
// traced back to a conversation. Retrying hands the task over again, which is
// the only way left to get an answer.
const lostConversationReason = "the gateway cannot tell which conversation this task runs in; retry to hand it to the agent again"

// adoptConversation fills in the conversation a delivered task actually runs
// in. Gateways older than the initial_task_conversation column handed tasks to
// the agent without recording the conversation, and those VMs would otherwise
// report "handed to the agent" for good: nothing polls them, and only a failed
// task can be retried. On c the conversation is left empty when the task was
// given up on, so the caller stops rather than polling nothing.
func adoptConversation(ctx context.Context, rt runtime.ContainerRuntime, database *db.DB, c *db.Container) error {
	if strings.TrimSpace(c.InitialTask) == "" {
		// Nothing to look up and nothing to retry either, so the state is left
		// exactly as it is rather than pointing the owner at a dead button.
		return nil
	}
	found, err := findConversationByTask(ctx, rt, c.IncusName, c.InitialTask)
	if err != nil {
		return fmt.Errorf("find the conversation of the task on %s: %w", c.IncusName, err)
	}
	if found == "" {
		if err := database.SetInitialTaskState(c.ID, db.TaskFailed, lostConversationReason); err != nil {
			return fmt.Errorf("record the lost task on %s: %w", c.IncusName, err)
		}
		c.InitialTaskState = db.TaskFailed
		c.InitialTaskError = lostConversationReason
		log.Printf("picoclaw: task on %s has no conversation in the VM; owner has to retry it", c.IncusName)
		return nil
	}
	if err := database.SetInitialTaskConversation(c.ID, found); err != nil {
		return fmt.Errorf("record the conversation of the task on %s: %w", c.IncusName, err)
	}
	c.InitialTaskConversation = found
	log.Printf("picoclaw: task on %s runs in conversation %s", c.IncusName, found)
	return nil
}

// findConversationByTask returns the conversation whose opening request is this
// VM's task, or "" when the agent has no such conversation. The task is
// user-supplied text, so it travels as a hex literal: nothing but [0-9a-f]
// reaches the VM, and there is no quoting left for it to escape.
func findConversationByTask(ctx context.Context, rt runtime.ContainerRuntime, incusName, task string) (string, error) {
	// sequence_id is numbered per conversation, so a conversation's opening
	// request is the lowest one of its own. Should several conversations open
	// with the same text, the newest is the one the last delivery started.
	query := fmt.Sprintf(
		`SELECT conversation_id FROM messages AS m WHERE type='user'`+
			` AND json_extract(llm_data, '$.Content[0].Text') = CAST(x'%s' AS TEXT)`+
			` AND sequence_id = (SELECT MIN(sequence_id) FROM messages AS m2`+
			` WHERE m2.conversation_id = m.conversation_id AND m2.type='user')`+
			` ORDER BY created_at DESC, rowid DESC LIMIT 1;`,
		hex.EncodeToString([]byte(task)))
	out, err := queryAgentDB(ctx, rt, incusName, query)
	if err != nil {
		return "", err
	}
	found := strings.TrimSpace(string(out))
	if found == "" {
		return "", nil
	}
	if !validConversationID(found) {
		return "", fmt.Errorf("agent named conversation %q", found)
	}
	return found, nil
}

// queryAgentDB asks the agent's own database a question. It is opened
// read-only: watching a task must not disturb the agent, and must not bring a
// missing database into existence either — sqlite3 creates an empty file for a
// failing read, and an empty file at DBPath would silently defeat the
// shelley.db migration a VM that has not been set up yet still needs.
func queryAgentDB(ctx context.Context, rt runtime.ContainerRuntime, incusName, query string) ([]byte, error) {
	return rt.Exec(ctx, incusName, []string{"sqlite3", "-readonly", DBPath, query})
}

// pollTaskState reads the agent's own database. agent_working is the flag the
// agent loop keeps while a turn runs, and the last message of the conversation
// says how the turn ended: an "agent" message is a completed answer, an
// "error" message is a failure the owner has to see.
func pollTaskState(ctx context.Context, rt runtime.ContainerRuntime, incusName, conversationID string) (state, reason string, err error) {
	query := fmt.Sprintf(
		`SELECT COALESCE((SELECT agent_working FROM conversations WHERE conversation_id='%[1]s'), -1)`+
			` || '|' || COALESCE((SELECT type FROM messages WHERE conversation_id='%[1]s' ORDER BY sequence_id DESC LIMIT 1), '');`,
		conversationID)
	out, err := queryAgentDB(ctx, rt, incusName, query)
	if err != nil {
		return "", "", err
	}
	working, lastMessage, ok := strings.Cut(strings.TrimSpace(string(out)), "|")
	if !ok {
		return "", "", fmt.Errorf("unexpected progress reply %q", strings.TrimSpace(string(out)))
	}

	switch working {
	case "-1":
		// The conversation is gone: deleted in the agent UI, or lost with a
		// restored database. Nothing will ever finish it.
		return db.TaskFailed, "the conversation this task ran in is no longer in the VM", nil
	case "1":
		return db.TaskWorking, "", nil
	case "0":
	default:
		return "", "", fmt.Errorf("unexpected agent_working value %q", working)
	}

	switch lastMessage {
	case "agent":
		return db.TaskDone, "", nil
	case "error":
		return db.TaskFailed, taskErrorText(ctx, rt, incusName, conversationID), nil
	default:
		// The agent clears agent_working for every unfinished turn on startup
		// and does not resume it, so an idle conversation that never produced
		// an answer was cut short rather than merely slow.
		return db.TaskFailed, "the agent stopped before answering; the VM or the agent restarted mid-task", nil
	}
}

// taskErrorText returns the agent's own wording for the failure, so the owner
// sees the same message the agent UI shows.
func taskErrorText(ctx context.Context, rt runtime.ContainerRuntime, incusName, conversationID string) string {
	const fallback = "the agent ended the task with an error"
	query := fmt.Sprintf(
		`SELECT COALESCE(json_extract(llm_data, '$.Content[0].Text'), '') FROM messages`+
			` WHERE conversation_id='%s' AND type='error' ORDER BY sequence_id DESC LIMIT 1;`,
		conversationID)
	out, err := queryAgentDB(ctx, rt, incusName, query)
	if err != nil {
		return fallback
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return fallback
	}
	if len(text) > maxTaskErrorLen {
		text = strings.TrimSpace(text[:maxTaskErrorLen]) + "…"
	}
	return text
}
