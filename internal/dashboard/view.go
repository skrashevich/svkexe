package dashboard

import (
	"strconv"
	"strings"
	"time"

	dbpkg "github.com/skrashevich/svkexe/internal/db"
)

// taskStep is one stop on the initial-task progress track.
type taskStep struct {
	Label string
	// State is "done", "current", "todo" or "failed"; the template styles by it.
	State string
	Last  bool
}

// taskInfo is everything the dashboard says about a VM's initial task. It is
// derived in one place so the list row and the detail page cannot disagree
// about whether a task is running, on hold or finished.
type taskInfo struct {
	Has bool
	// Label is the short state shown next to the task.
	Label string
	// Tone is "accent", "ok", "danger" or "mute".
	Tone string
	// Spin marks a task the agent is actively moving on.
	Spin bool
	// OnHold marks a handed-over task whose VM is not running: no agent is
	// there to work on it, and the next start picks it back up.
	OnHold bool
	Failed bool
	// CanWatch reports that there is a live conversation worth opening.
	CanWatch bool
	Steps    []taskStep
}

// vmTask resolves the task state against the VM status. A VM that is still
// being created is not off: delivery runs before it is marked running, so a
// task handed over then really is with the agent.
func vmTask(c *dbpkg.Container) taskInfo {
	if c == nil || c.InitialTaskState == "" {
		return taskInfo{Label: "No task", Tone: "none"}
	}
	t := taskInfo{Has: true}
	vmUp := c.Status == "running" || c.Status == "creating"
	idx := 0
	switch c.InitialTaskState {
	case dbpkg.TaskPending:
		t.Label, t.Tone, idx = "Waiting for the VM", "mute", 0
	case dbpkg.TaskSent:
		t.Label, t.Tone, t.Spin, idx = "Handed to the agent", "accent", true, 1
	case dbpkg.TaskWorking:
		t.Label, t.Tone, t.Spin, idx = "Agent is working", "accent", true, 2
	case dbpkg.TaskDone:
		t.Label, t.Tone, idx = "Finished", "ok", 3
	default:
		t.Label, t.Tone, t.Failed, idx = "Did not finish", "danger", true, 2
	}
	if t.Spin && !vmUp {
		t.Label, t.Tone, t.Spin, t.OnHold = "On hold", "mute", false, true
	}
	// A failed task's conversation is the one most worth reading, so any task
	// with a conversation on a running VM can be watched.
	t.CanWatch = c.InitialTaskConversation != "" && c.Status == "running"

	for i, label := range []string{"Queued", "Sent", "Working", "Done"} {
		s := taskStep{Label: label, State: "todo", Last: i == 3}
		switch {
		case t.Failed && i == idx:
			s.Label, s.State = "Failed", "failed"
		case i < idx || (i == idx && c.InitialTaskState == dbpkg.TaskDone):
			s.State = "done"
		case i == idx:
			s.State = "current"
		}
		t.Steps = append(t.Steps, s)
	}
	return t
}

// statusLabel turns a stored VM status into the word the dashboard shows.
func statusLabel(status string) string {
	if status == "" {
		return "Unknown"
	}
	return strings.ToUpper(status[:1]) + status[1:]
}

// memSize renders a memory limit the way people read it: 2 GB, 512 MB.
func memSize(mb int) string {
	if mb >= 1024 && mb%1024 == 0 {
		return strconv.Itoa(mb/1024) + " GB"
	}
	return strconv.Itoa(mb) + " MB"
}

// shortDate is the compact date used in lists, with the year only when it is
// not the current one.
func shortDate(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	if t.Year() == time.Now().Year() {
		return t.Format("Jan 02")
	}
	return t.Format("Jan 02, 2006")
}

// initial is the avatar letter for an email or a name.
func initial(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "?"
	}
	return strings.ToUpper(s[:1])
}

func roleLabel(role string) string {
	switch role {
	case "admin":
		return "Administrator"
	case dbpkg.GuestRole:
		return "Guest"
	default:
		return "Member"
	}
}

// providerLabel names a stored LLM connection. Custom connections are stored
// as "custom-<name>" and are known to their owner by that name alone.
func providerLabel(provider string) string {
	if name, ok := strings.CutPrefix(provider, "custom-"); ok {
		return name
	}
	switch provider {
	case "openrouter":
		return "OpenRouter"
	case "openai":
		return "OpenAI"
	case "anthropic":
		return "Anthropic"
	case "gemini":
		return "Gemini"
	case "fireworks":
		return "Fireworks"
	}
	return provider
}

// vmCardData backs the VM detail fragment. The container's own fields are
// promoted, so the template reads .Name and .Status as it would on the row.
type vmCardData struct {
	*dbpkg.Container
	// Members is who else may use this VM; only its owner is shown the list.
	Members []*dbpkg.User
	// AccessError is a refused invitation, rendered in the Access tab, and
	// AccessEmail the address it was for, so the owner does not retype it.
	AccessError string
	AccessEmail string
}

// vmPageData is the full detail page: layout chrome plus the fragment.
type vmPageData struct {
	templateData
	Card vmCardData
}
