package picoclaw

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
)

// TaskPollInterval is how often a delivered task is checked for progress. The
// dashboard reloads every 5s, so this is the resolution the owner sees.
const TaskPollInterval = 15 * time.Second

// taskPollTimeout bounds one round with a VM. A VM that cannot answer in time
// keeps its recorded state and is asked again on the next round. Resuming a task
// is the longest round: the progress query, the failure details, the resume
// itself and the failure text, so this has to leave room for all of them rather
// than for a single query.
const taskPollTimeout = 45 * time.Second

// maxTaskErrorLen keeps an agent error readable in the VM card.
const maxTaskErrorLen = 500

// maxTaskResumes bounds how many times the gateway picks a task back up after
// the agent's turn died on a failure the agent itself calls transient. A
// provider hiccup clears in an attempt or two; a task that keeps dying the same
// way is a real failure the owner has to see, not a loop to feed forever.
//
// This is the only brake on everything below it. Each resume is a whole turn,
// and a turn is already several LLM requests: the agent loop retries twice, and
// some providers retry inside that again. Raising this multiplies through all of
// them, so raise it only with that arithmetic in hand.
const maxTaskResumes = 3

// taskResumeTimeout bounds the resume request. The agent answers it before the
// resumed turn runs, so this only has to cover loading the conversation.
const taskResumeTimeout = 15 * time.Second

// maxTaskResumeAge is how old a failure may be for the gateway to pick it back
// up on its own. Polling is a matter of seconds, so a live hiccup is always well
// inside this; the window exists so that a turn which died while the gateway was
// down — or one on a VM whose conversation was only just traced back, possibly
// months after the task was handed over — is reported to the owner instead of
// waking an install nobody is watching.
const maxTaskResumeAge = time.Hour

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

	state, reason, err := pollTaskState(ctx, rt, database, c)
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
//
// A "slug" message is skipped, exactly as the agent skips it when it decides
// whether to offer Retry. Naming a conversation is a separate LLM call that
// races the first turn, so its marker can land after the turn has already
// finished; counting it as the last message would report a finished task as cut
// short, and would hide a failure the gateway could otherwise resume.
func pollTaskState(ctx context.Context, rt runtime.ContainerRuntime, database *db.DB, c *db.Container) (state, reason string, err error) {
	incusName, conversationID := c.IncusName, c.InitialTaskConversation
	query := fmt.Sprintf(
		`SELECT COALESCE((SELECT agent_working FROM conversations WHERE conversation_id='%[1]s'), -1)`+
			` || '|' || COALESCE((SELECT type FROM messages WHERE conversation_id='%[1]s'`+
			` AND type != 'slug' ORDER BY sequence_id DESC LIMIT 1), '');`,
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
		return resolveFailedTurn(ctx, rt, database, c)
	default:
		// The agent clears agent_working for every unfinished turn on startup
		// and does not resume it, so an idle conversation that never produced
		// an answer was cut short rather than merely slow.
		return db.TaskFailed, "the agent stopped before answering; the VM or the agent restarted mid-task", nil
	}
}

// resolveFailedTurn decides what a turn that ended on an error means for the
// task. A cut stream or a provider hiccup is not the end of the job: the agent
// marks such a failure retryable, and asking it to pick the conversation back
// up continues from the work already done instead of throwing it away and
// waiting for the owner to notice. Only a failure the agent calls permanent, or
// one that keeps coming back, is reported as a failed task.
func resolveFailedTurn(ctx context.Context, rt runtime.ContainerRuntime, database *db.DB, c *db.Container) (state, reason string, err error) {
	turn, err := failedTurnDetails(ctx, rt, c.IncusName, c.InitialTaskConversation)
	if err != nil {
		return "", "", err
	}
	if turn.resumable(c.InitialTaskResumes) {
		resumed, err := resumeConversation(ctx, rt, c)
		if err != nil {
			// The request never reached the agent, so nothing was spent: a VM
			// that is briefly out of reach while it reboots must not be able to
			// exhaust the budget of a task nobody ever asked to resume. The next
			// round asks again, and maxTaskResumeAge ends it either way.
			return "", "", err
		}
		// The agent answered, so the attempt is charged whether or not it took
		// the task back. A resume the agent declines leaves its own message log
		// untouched, and only this count can stop the gateway asking forever.
		spent, err := database.CountInitialTaskResume(c.ID)
		if err != nil {
			// The request has already gone out, so the failure is in the
			// bookkeeping, not in the resume. Reporting the turn as running is
			// the honest answer; refusing to record it would say the task is
			// still waiting while the agent works on it, and would ask again on
			// the next round with nothing counted.
			log.Printf("picoclaw: %s resumed its task but the attempt could not be counted: %v", c.IncusName, err)
			if resumed {
				return db.TaskWorking, "", nil
			}
			return "", "", err
		}
		c.InitialTaskResumes = spent
		if resumed {
			log.Printf("picoclaw: asked %s to resume its task after a transient agent failure (resume %d of %d)", c.IncusName, spent, maxTaskResumes)
			return db.TaskWorking, "", nil
		}
	}
	text := taskErrorText(ctx, rt, c.IncusName, c.InitialTaskConversation)
	if turn.retryable && c.InitialTaskResumes >= maxTaskResumes {
		text = fmt.Sprintf("%s (resumed %d times without getting through)", text, c.InitialTaskResumes)
	}
	return db.TaskFailed, text, nil
}

// failedTurn is what the agent's database says about the failure a task's turn
// ended on.
type failedTurn struct {
	// retryable is the agent's own verdict, stored on the error message.
	retryable bool
	// age is how long ago the failure was recorded, measured by the VM's own
	// clock so gateway and guest never have to agree on the time.
	age time.Duration
}

// resumable reports whether the gateway should pick this failure back up
// instead of handing it to the owner. spent is what the task's budget has
// already cost.
func (t failedTurn) resumable(spent int) bool {
	return t.retryable && spent < maxTaskResumes && t.age <= maxTaskResumeAge
}

// failedTurnDetails reads the agent's verdict on the latest failure and how old
// it is. The age is computed inside the VM, so the gateway and the guest never
// have to agree on the time; an undatable failure answers "unknown" rather than
// a number, and is treated as too old to touch.
func failedTurnDetails(ctx context.Context, rt runtime.ContainerRuntime, incusName, conversationID string) (failedTurn, error) {
	const latest = `(SELECT %s FROM messages WHERE conversation_id='%s' AND type='error' ORDER BY sequence_id DESC LIMIT 1)`
	query := fmt.Sprintf(
		`SELECT COALESCE(%[1]s, 0) || '|' || COALESCE(%[2]s, 'unknown');`,
		fmt.Sprintf(latest, `json_extract(user_data, '$.retryable')`, conversationID),
		fmt.Sprintf(latest, `CAST(MAX(0, (julianday('now') - julianday(created_at)) * 86400) AS INTEGER)`, conversationID),
	)
	out, err := queryAgentDB(ctx, rt, incusName, query)
	if err != nil {
		return failedTurn{}, err
	}
	flag, age, ok := strings.Cut(strings.TrimSpace(string(out)), "|")
	if !ok {
		return failedTurn{}, fmt.Errorf("unexpected failure reply %q", strings.TrimSpace(string(out)))
	}
	turn := failedTurn{retryable: flag == "1", age: maxTaskResumeAge + time.Second}
	if age != "unknown" {
		seconds, err := strconv.Atoi(age)
		if err != nil {
			return failedTurn{}, fmt.Errorf("unexpected failure age %q", age)
		}
		turn.age = time.Duration(seconds) * time.Second
	}
	return turn, nil
}

// resumeConversation asks the agent to re-run the request its turn died on. It
// reports whether the agent took the task back; an agent that refuses is not an
// error to raise, it is the answer that this failure is final. A VM that cannot
// be reached at all is an error, so the task keeps its state and is asked again
// on the next round.
func resumeConversation(ctx context.Context, rt runtime.ContainerRuntime, c *db.Container) (bool, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/api/conversation/%s/retry", Port, c.InitialTaskConversation)
	out, err := rt.Exec(ctx, c.IncusName, []string{
		"curl", "--silent", "--show-error", "--max-time", strconv.Itoa(int(taskResumeTimeout.Seconds())),
		"--output", "/dev/null", "--write-out", "%{http_code}",
		"--request", "POST", "--header", RequireHeader + ": " + c.OwnerID, url,
	})
	if err != nil {
		return false, fmt.Errorf("ask %s to resume conversation %s: %w", c.IncusName, c.InitialTaskConversation, err)
	}
	switch code := strings.TrimSpace(string(out)); code {
	case "200", "202":
		return true, nil
	default:
		log.Printf("picoclaw: %s refused to resume conversation %s (HTTP %s)", c.IncusName, c.InitialTaskConversation, code)
		return false, nil
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
