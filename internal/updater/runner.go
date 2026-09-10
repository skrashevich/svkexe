package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Update run states, as written by scripts/update.sh into the status file.
const (
	StateIdle    = "idle"
	StateRunning = "running"
	StateSuccess = "success"
	StateFailed  = "failed"
)

// Default paths for the trigger and status files, also used by
// NewRunnerFromEnv.
const (
	DefaultTriggerPath = "/var/lib/svkexe/update.trigger"
	DefaultStatusPath  = "/var/lib/svkexe/update-status.json"
)

// Sentinel errors returned by Start, wrapped with context and checkable with
// errors.Is.
var (
	// ErrUpdateInProgress means an update is already running; starting a second
	// one would fight over the binary and the systemd unit.
	ErrUpdateInProgress = errors.New("update already in progress")
	// ErrUpdateUnavailable means this deployment cannot self-update, e.g. the
	// trigger directory does not exist or is not writable.
	ErrUpdateUnavailable = errors.New("update not available in this deployment")
)

// RunnerConfig describes how the gateway asks for a privileged update.
//
// The gateway unit runs as User=svkexe with NoNewPrivileges=true, so it can
// never elevate. Instead it drops a trigger file that a root-owned
// svkexe-update.path unit watches; that unit starts svkexe-update.service,
// which runs scripts/update.sh and restarts the gateway. Because the gateway
// is restarted by its own update, progress is reported through a file on disk
// rather than through the process tree.
type RunnerConfig struct {
	// TriggerPath is the file whose creation starts the update.
	TriggerPath string
	// StatusPath is the JSON file the update script writes progress into.
	StatusPath string
	// Command, when set, is executed directly instead of using the trigger
	// file. It exists for deployments where the gateway is already privileged,
	// such as a Docker image or a developer machine.
	Command []string
	// StaleAfter bounds how long a run may claim to be running. A trigger that
	// nothing picked up — the path unit is not installed, the update host was
	// rebooted mid-build — would otherwise wedge the state at "running" and
	// block every later update. Defaults to DefaultStaleAfter.
	StaleAfter time.Duration
}

// DefaultStaleAfter is the ceiling on a plausible update: a full rebuild of the
// gateway plus the agent UI takes minutes, never hours.
const DefaultStaleAfter = 2 * time.Hour

// Runner starts privileged updates and reports on their progress.
type Runner struct {
	cfg RunnerConfig
}

// NewRunner returns a Runner, filling empty paths with the documented
// defaults.
func NewRunner(cfg RunnerConfig) *Runner {
	if cfg.TriggerPath == "" {
		cfg.TriggerPath = DefaultTriggerPath
	}
	if cfg.StatusPath == "" {
		cfg.StatusPath = DefaultStatusPath
	}
	if cfg.StaleAfter <= 0 {
		cfg.StaleAfter = DefaultStaleAfter
	}
	return &Runner{cfg: cfg}
}

// NewRunnerFromEnv builds a Runner from the environment:
//
//	SVKEXE_UPDATE_TRIGGER  (default /var/lib/svkexe/update.trigger)
//	SVKEXE_UPDATE_STATUS   (default /var/lib/svkexe/update-status.json)
//	SVKEXE_UPDATE_COMMAND  optional command run directly, split on whitespace
func NewRunnerFromEnv() *Runner {
	return NewRunner(RunnerConfig{
		TriggerPath: os.Getenv("SVKEXE_UPDATE_TRIGGER"),
		StatusPath:  os.Getenv("SVKEXE_UPDATE_STATUS"),
		Command:     strings.Fields(os.Getenv("SVKEXE_UPDATE_COMMAND")),
	})
}

// Config returns the effective configuration, with defaults applied.
func (r *Runner) Config() RunnerConfig { return r.cfg }

// RunState is the progress record the update script keeps on disk. It outlives
// the gateway process, which the update itself restarts.
type RunState struct {
	State      string    `json:"state"`
	StartedAt  time.Time `json:"startedAt,omitempty"`
	FinishedAt time.Time `json:"finishedAt,omitempty"`
	Commit     string    `json:"commit,omitempty"`
	Error      string    `json:"error,omitempty"`
	// Log is a bounded tail of the update output, for display in the
	// dashboard.
	Log string `json:"log,omitempty"`
}

// Running reports whether an update is currently executing.
func (r RunState) Running() bool { return r.State == StateRunning }

// Terminal reports whether the run has finished, successfully or not.
func (r RunState) Terminal() bool { return r.State == StateSuccess || r.State == StateFailed }

// Available reports whether this deployment can start an update. The second
// return value is a human-readable reason when it cannot; an unsupported
// deployment is a normal state, not an error.
func (r *Runner) Available() (bool, string) {
	if len(r.cfg.Command) > 0 {
		return true, ""
	}
	dir := filepath.Dir(r.cfg.TriggerPath)
	info, err := os.Stat(dir)
	if err != nil {
		return false, fmt.Sprintf("update trigger directory %s is not accessible", dir)
	}
	if !info.IsDir() {
		return false, fmt.Sprintf("update trigger path %s is not a directory", dir)
	}
	// Permission bits alone do not answer this (supplementary groups, ACLs,
	// read-only mounts), so probe with a real file.
	probe, err := os.CreateTemp(dir, ".svkexe-update-probe-*")
	if err != nil {
		return false, fmt.Sprintf("update trigger directory %s is not writable", dir)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return true, ""
}

// Status reads the update status file. A missing file means no update has ever
// run and reports StateIdle. A malformed file is reported as a failed state
// carrying the parse error, so a corrupt file cannot take the dashboard down.
func (r *Runner) Status() (RunState, error) {
	data, err := os.ReadFile(r.cfg.StatusPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RunState{State: StateIdle}, nil
		}
		return RunState{State: StateIdle}, fmt.Errorf("read update status %s: %w", r.cfg.StatusPath, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return RunState{State: StateIdle}, nil
	}

	var st RunState
	if err := json.Unmarshal(data, &st); err != nil {
		return RunState{
			State: StateFailed,
			Error: fmt.Sprintf("malformed update status %s: %v", r.cfg.StatusPath, err),
		}, nil
	}
	if st.State == "" {
		st.State = StateIdle
	}
	if st.Running() && !st.StartedAt.IsZero() && time.Since(st.StartedAt) > r.cfg.StaleAfter {
		st.State = StateFailed
		st.Error = fmt.Sprintf("update has been running since %s with no result; treating it as failed", st.StartedAt.UTC().Format(time.RFC3339))
	}
	return st, nil
}

// Start asks for an update to be performed. It returns as soon as the request
// has been placed: the update outlives this process, which it restarts.
func (r *Runner) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("start update: %w", err)
	}
	if st, _ := r.Status(); st.Running() {
		return ErrUpdateInProgress
	}
	if ok, reason := r.Available(); !ok {
		return fmt.Errorf("%w: %s", ErrUpdateUnavailable, reason)
	}

	if len(r.cfg.Command) > 0 {
		return r.startCommand()
	}
	return r.writeTrigger()
}

// startCommand runs the configured command detached from the gateway. The
// update restarts the gateway, so the child must survive its parent: it gets
// its own process group and is deliberately not bound to the request context.
func (r *Runner) startCommand() error {
	cmd := exec.Command(r.cfg.Command[0], r.cfg.Command[1:]...) // #nosec G204 -- operator-supplied command from the environment
	cmd.SysProcAttr = detachedSysProcAttr()
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start update command %q: %w", r.cfg.Command[0], err)
	}
	// Reap the child so it does not linger as a zombie if the gateway happens
	// to outlive it.
	go func() { _ = cmd.Wait() }()
	return nil
}

// triggerPayload is the small record written into the trigger file. The
// watching systemd unit only cares that the file appeared; the timestamp is
// there for operators reading it after the fact.
type triggerPayload struct {
	RequestedAt time.Time `json:"requestedAt"`
}

func (r *Runner) writeTrigger() error {
	now := time.Now().UTC()
	payload, err := json.Marshal(triggerPayload{RequestedAt: now})
	if err != nil {
		return fmt.Errorf("encode update trigger: %w", err)
	}
	if err := writeFileAtomic(r.cfg.TriggerPath, append(payload, '\n'), 0o644); err != nil {
		return fmt.Errorf("write update trigger %s: %w", r.cfg.TriggerPath, err)
	}

	// Seed a running state so the dashboard shows progress in the window
	// between the trigger landing and the script's first status write. Best
	// effort: the status file usually belongs to root, and failing to seed it
	// must not fail an update that has already been requested.
	seed, err := json.Marshal(RunState{State: StateRunning, StartedAt: now})
	if err == nil {
		_ = writeFileAtomic(r.cfg.StatusPath, append(seed, '\n'), 0o644)
	}
	return nil
}

// writeFileAtomic writes to a temporary file in the destination directory and
// renames it into place, so a watcher never observes a half-written file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmp := f.Name()
	defer func() {
		if tmp != "" {
			_ = os.Remove(tmp)
		}
	}()

	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write temp file %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp file %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return fmt.Errorf("chmod temp file %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmp, path, err)
	}
	tmp = ""
	return nil
}
