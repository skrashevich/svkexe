package sshgw

import (
	"time"

	"github.com/skrashevich/svkexe/internal/db"
)

// timeJSON is how every view renders a timestamp: one format, declared once, so
// a caller parsing one command's output can parse them all.
const timeJSON = "2006-01-02T15:04:05Z"

func formatTime(t time.Time) string { return t.UTC().Format(timeJSON) }

// formatExpiry renders a moment for a human, in UTC because the two places that
// print one — creating a share link and listing them — must not disagree about
// which clock the answer is on.
func formatExpiry(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }

// The view types are what --json emits. They are declared once, as Go structs,
// so that the field names an agent parses cannot drift from one command to the
// next and cannot be broken by an edit to a format string.

// vmJSON describes one VM.
type vmJSON struct {
	Name             string    `json:"name"`
	Status           string    `json:"status"`
	CPU              int       `json:"cpu"`
	MemoryMB         int       `json:"memory_mb"`
	DiskGB           int       `json:"disk_gb"`
	IP               string    `json:"ip,omitempty"`
	URL              string    `json:"url,omitempty"`
	AppPort          int       `json:"app_port"`
	AppPublic        bool      `json:"app_public"`
	Nesting          bool      `json:"nesting"`
	NestingEffective bool      `json:"nesting_effective"`
	NestingPending   bool      `json:"nesting_pending"`
	Task             *taskJSON `json:"task,omitempty"`
	Aliases          []string  `json:"aliases,omitempty"`
	CreatedAt        string    `json:"created_at"`
}

// taskJSON describes the one-off task handed to a VM's agent at first boot.
type taskJSON struct {
	State        string `json:"state"`
	Text         string `json:"text,omitempty"`
	Error        string `json:"error,omitempty"`
	Conversation string `json:"conversation,omitempty"`
	Resumes      int    `json:"resumes"`
}

// newVMJSON renders a container. The nesting and alias fields are only
// meaningful for a container the caller has filled in with AttachNestingPolicy
// and AttachAliases; both default to the conservative answer otherwise.
func newVMJSON(c *db.Container, domain string) vmJSON {
	v := vmJSON{
		Name:             c.Name,
		Status:           c.Status,
		CPU:              c.CPULimit,
		MemoryMB:         c.MemoryMB,
		DiskGB:           c.DiskGB,
		IP:               c.IPAddress,
		URL:              vmURL(domain, c.Name),
		AppPort:          c.AppPort,
		AppPublic:        c.AppPublic,
		Nesting:          c.Nesting,
		NestingEffective: c.NestingEffective(),
		NestingPending:   c.NestingPending(),
		CreatedAt:        formatTime(c.CreatedAt),
	}
	if c.InitialTaskState != "" {
		v.Task = &taskJSON{
			State:        c.InitialTaskState,
			Text:         c.InitialTask,
			Error:        c.InitialTaskError,
			Conversation: c.InitialTaskConversation,
			Resumes:      c.InitialTaskResumes,
		}
	}
	for _, a := range c.Aliases {
		v.Aliases = append(v.Aliases, a.Hostname)
	}
	return v
}

// vmURL is the address a VM's workload answers on. It is empty when the
// deployment has no domain configured, in which case there is no such address.
func vmURL(domain, name string) string {
	if domain == "" {
		return ""
	}
	return "https://" + name + "." + domain
}

// The formatters below are shared by every command that renders a VM, so that
// "public", "pending restart" and "verified" read the same wherever they appear.

// appPort is the port the VM's host actually proxies to: a container that has
// never been published carries a zero, which means the platform default.
func appPort(c *db.Container) int {
	if c.AppPort == 0 {
		return db.DefaultAppPort
	}
	return c.AppPort
}

func visibility(public bool) string {
	if public {
		return "public"
	}
	return "session required"
}

// nestingSummary states both halves of the nesting answer: what is in force and
// whether the VM still owes a restart before it is true of the running instance.
func nestingSummary(c *db.Container) string {
	state := "off"
	if c.NestingEffective() {
		state = "on"
	}
	switch {
	case c.NestingPending():
		return state + " (restart pending)"
	case c.Nesting && !c.NestingAllowed:
		return state + " (the deployment forbids nested containers)"
	default:
		return state
	}
}

func aliasState(a *db.ContainerAlias) string {
	if a.Verified {
		return "verified"
	}
	if a.LastError != "" {
		return "unverified: " + a.LastError
	}
	return "unverified"
}

// taskSummary renders the state of the one-off task handed to the agent.
func taskSummary(c *db.Container) string {
	if c.InitialTaskState == "" {
		return "none"
	}
	summary := c.InitialTaskState
	if c.InitialTaskError != "" {
		summary += ": " + c.InitialTaskError
	}
	return summary
}
