package updater

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestRunner returns a Runner rooted in a temporary directory, so tests
// never touch /var/lib/svkexe.
func newTestRunner(t *testing.T) (*Runner, string) {
	t.Helper()
	dir := t.TempDir()
	return NewRunner(RunnerConfig{
		TriggerPath: filepath.Join(dir, "update.trigger"),
		StatusPath:  filepath.Join(dir, "update-status.json"),
	}), dir
}

// runningStatus renders a "running" status document that started `age` ago.
// The age is relative to now rather than a fixed date, because Status() ages
// a stuck run out into StateFailed after RunnerConfig.StaleAfter.
func runningStatus(age time.Duration) string {
	started := time.Now().UTC().Add(-age).Format(time.RFC3339)
	return `{"state":"running","startedAt":"` + started + `"}`
}

func writeStatus(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write status file: %v", err)
	}
}

func TestRunnerStartWritesTrigger(t *testing.T) {
	r, dir := newTestRunner(t)

	before := time.Now().UTC().Add(-time.Second)
	if err := r.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "update.trigger"))
	if err != nil {
		t.Fatalf("read trigger: %v", err)
	}
	var payload triggerPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode trigger %q: %v", data, err)
	}
	if payload.RequestedAt.Before(before) {
		t.Errorf("RequestedAt = %v, want a recent timestamp", payload.RequestedAt)
	}

	st, err := r.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Running() {
		t.Errorf("seeded state = %q, want %q", st.State, StateRunning)
	}
	if st.StartedAt.IsZero() {
		t.Error("seeded StartedAt is zero")
	}

	// The atomic write must not leave temporary files behind for the systemd
	// path unit to trip over.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file %q", e.Name())
		}
	}
}

func TestRunnerStartRejectsConcurrentRun(t *testing.T) {
	r, dir := newTestRunner(t)
	writeStatus(t, filepath.Join(dir, "update-status.json"), runningStatus(time.Minute))

	err := r.Start(t.Context())
	if !errors.Is(err, ErrUpdateInProgress) {
		t.Fatalf("Start error = %v, want ErrUpdateInProgress", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "update.trigger")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("trigger file was written despite a running update (stat err %v)", err)
	}
}

// A trigger that nothing consumed — the path unit is not installed, the host
// rebooted mid-build — must not lock the admin out of ever updating again.
func TestRunnerStartAfterStaleRun(t *testing.T) {
	r, dir := newTestRunner(t)
	writeStatus(t, filepath.Join(dir, "update-status.json"), runningStatus(DefaultStaleAfter+time.Minute))

	if err := r.Start(t.Context()); err != nil {
		t.Fatalf("Start after a stale run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "update.trigger")); err != nil {
		t.Errorf("trigger file missing: %v", err)
	}
}

func TestRunnerStartAfterTerminalRun(t *testing.T) {
	for _, state := range []string{StateIdle, StateSuccess, StateFailed} {
		t.Run(state, func(t *testing.T) {
			r, dir := newTestRunner(t)
			writeStatus(t, filepath.Join(dir, "update-status.json"), `{"state":"`+state+`"}`)

			if err := r.Start(t.Context()); err != nil {
				t.Fatalf("Start after %s: %v", state, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "update.trigger")); err != nil {
				t.Errorf("trigger file missing: %v", err)
			}
		})
	}
}

func TestRunnerStatus(t *testing.T) {
	tests := []struct {
		name         string
		body         string // empty means: do not create the file
		wantState    string
		wantRunning  bool
		wantTerminal bool
		wantErrText  bool
		wantCommit   string
	}{
		{name: "missing file is idle", wantState: StateIdle},
		{name: "empty file is idle", body: "  \n", wantState: StateIdle},
		{name: "idle", body: `{"state":"idle"}`, wantState: StateIdle},
		{
			name:        "running",
			body:        runningStatus(time.Minute),
			wantState:   StateRunning,
			wantRunning: true,
		},
		{
			// A trigger nothing ever picked up must not wedge the state at
			// "running" and lock out every later update.
			name:         "stale running is reported as failed",
			body:         runningStatus(DefaultStaleAfter + time.Minute),
			wantState:    StateFailed,
			wantTerminal: true,
			wantErrText:  true,
		},
		{
			name:         "success",
			body:         `{"state":"success","startedAt":"2026-09-10T10:00:00Z","finishedAt":"2026-09-10T10:05:00Z","commit":"` + remoteSHA + `","log":"done"}`,
			wantState:    StateSuccess,
			wantTerminal: true,
			wantCommit:   remoteSHA,
		},
		{
			name:         "failed",
			body:         `{"state":"failed","error":"build failed","log":"make: *** [build] Error 1"}`,
			wantState:    StateFailed,
			wantTerminal: true,
			wantErrText:  true,
		},
		{
			name:         "malformed json",
			body:         `{"state": "run`,
			wantState:    StateFailed,
			wantTerminal: true,
			wantErrText:  true,
		},
		{name: "missing state field defaults to idle", body: `{"commit":"abc"}`, wantState: StateIdle, wantCommit: "abc"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, dir := newTestRunner(t)
			if tc.body != "" {
				writeStatus(t, filepath.Join(dir, "update-status.json"), tc.body)
			}

			st, err := r.Status()
			if err != nil {
				t.Fatalf("Status returned a hard error: %v", err)
			}
			if st.State != tc.wantState {
				t.Errorf("State = %q, want %q", st.State, tc.wantState)
			}
			if st.Running() != tc.wantRunning {
				t.Errorf("Running() = %v, want %v", st.Running(), tc.wantRunning)
			}
			if st.Terminal() != tc.wantTerminal {
				t.Errorf("Terminal() = %v, want %v", st.Terminal(), tc.wantTerminal)
			}
			if (st.Error != "") != tc.wantErrText {
				t.Errorf("Error = %q, want present=%v", st.Error, tc.wantErrText)
			}
			if st.Commit != tc.wantCommit {
				t.Errorf("Commit = %q, want %q", st.Commit, tc.wantCommit)
			}
		})
	}
}

func TestRunnerAvailable(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name        string
		cfg         RunnerConfig
		wantOK      bool
		wantReasonN string
	}{
		{
			name:   "writable trigger directory",
			cfg:    RunnerConfig{TriggerPath: filepath.Join(dir, "update.trigger")},
			wantOK: true,
		},
		{
			name:        "missing trigger directory",
			cfg:         RunnerConfig{TriggerPath: filepath.Join(dir, "nope", "update.trigger")},
			wantReasonN: "not accessible",
		},
		{
			name:   "command overrides the trigger path",
			cfg:    RunnerConfig{TriggerPath: filepath.Join(dir, "nope", "update.trigger"), Command: []string{"/bin/true"}},
			wantOK: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRunner(tc.cfg)
			ok, reason := r.Available()
			if ok != tc.wantOK {
				t.Fatalf("Available() = %v (%q), want %v", ok, reason, tc.wantOK)
			}
			if ok {
				if reason != "" {
					t.Errorf("reason = %q, want empty when available", reason)
				}
				return
			}
			if !strings.Contains(reason, tc.wantReasonN) {
				t.Errorf("reason = %q, want it to mention %q", reason, tc.wantReasonN)
			}
			if !strings.Contains(reason, filepath.Dir(tc.cfg.TriggerPath)) {
				t.Errorf("reason = %q, want it to name the directory", reason)
			}
		})
	}
}

func TestRunnerAvailableRejectsFileAsDirectory(t *testing.T) {
	dir := t.TempDir()
	notADir := filepath.Join(dir, "file")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRunner(RunnerConfig{TriggerPath: filepath.Join(notADir, "update.trigger")})
	ok, reason := r.Available()
	if ok {
		t.Fatal("Available() = true for a file used as a directory")
	}
	if reason == "" {
		t.Error("reason is empty")
	}
}

func TestRunnerStartUnavailable(t *testing.T) {
	dir := t.TempDir()
	r := NewRunner(RunnerConfig{
		TriggerPath: filepath.Join(dir, "nope", "update.trigger"),
		StatusPath:  filepath.Join(dir, "update-status.json"),
	})

	err := r.Start(t.Context())
	if !errors.Is(err, ErrUpdateUnavailable) {
		t.Fatalf("Start error = %v, want ErrUpdateUnavailable", err)
	}
	if !strings.Contains(err.Error(), "not accessible") {
		t.Errorf("error = %q, want it to carry the reason", err)
	}
}

func TestRunnerStartSucceedsWithUnwritableStatusFile(t *testing.T) {
	dir := t.TempDir()
	// The status file normally belongs to root; a gateway that cannot seed it
	// must still be able to request the update.
	statusDir := filepath.Join(dir, "status")
	if err := os.Mkdir(statusDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(statusDir, 0o755) })

	r := NewRunner(RunnerConfig{
		TriggerPath: filepath.Join(dir, "update.trigger"),
		StatusPath:  filepath.Join(statusDir, "update-status.json"),
	})
	if err := r.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "update.trigger")); err != nil {
		t.Errorf("trigger file missing: %v", err)
	}
}

func TestRunnerStartCommand(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran.marker")
	r := NewRunner(RunnerConfig{
		TriggerPath: filepath.Join(dir, "update.trigger"),
		StatusPath:  filepath.Join(dir, "update-status.json"),
		Command:     []string{"/bin/sh", "-c", "printf updated > " + marker},
	})

	if err := r.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// The command path must not use the trigger mechanism.
	if _, err := os.Stat(filepath.Join(dir, "update.trigger")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("trigger file written on the command path (stat err %v)", err)
	}

	data := waitForFile(t, marker, 5*time.Second)
	if string(data) != "updated" {
		t.Errorf("marker = %q, want %q", data, "updated")
	}
}

func TestRunnerStartCommandFailure(t *testing.T) {
	dir := t.TempDir()
	r := NewRunner(RunnerConfig{
		TriggerPath: filepath.Join(dir, "update.trigger"),
		StatusPath:  filepath.Join(dir, "update-status.json"),
		Command:     []string{filepath.Join(dir, "definitely-not-here")},
	})

	err := r.Start(t.Context())
	if err == nil {
		t.Fatal("Start succeeded, want an error for a missing binary")
	}
	if !strings.Contains(err.Error(), "start update command") {
		t.Errorf("error = %q, want it wrapped with context", err)
	}
}

// waitForFile polls for path until it has content or the timeout expires,
// which keeps the test deterministic without sleeping for a fixed duration.
func waitForFile(t *testing.T, path string, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			return data
		}
		if time.Now().After(deadline) {
			t.Fatalf("file %s did not appear within %v (last err %v)", path, timeout, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestNewRunnerDefaults(t *testing.T) {
	cfg := NewRunner(RunnerConfig{}).Config()
	if cfg.TriggerPath != DefaultTriggerPath {
		t.Errorf("TriggerPath = %q, want %q", cfg.TriggerPath, DefaultTriggerPath)
	}
	if cfg.StatusPath != DefaultStatusPath {
		t.Errorf("StatusPath = %q, want %q", cfg.StatusPath, DefaultStatusPath)
	}
	if len(cfg.Command) != 0 {
		t.Errorf("Command = %v, want empty", cfg.Command)
	}
}

func TestNewRunnerFromEnv(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		wantTrigger string
		wantStatus  string
		wantCommand []string
	}{
		{
			name:        "defaults",
			wantTrigger: DefaultTriggerPath,
			wantStatus:  DefaultStatusPath,
		},
		{
			name: "explicit paths",
			env: map[string]string{
				"SVKEXE_UPDATE_TRIGGER": "/tmp/t.trigger",
				"SVKEXE_UPDATE_STATUS":  "/tmp/s.json",
			},
			wantTrigger: "/tmp/t.trigger",
			wantStatus:  "/tmp/s.json",
		},
		{
			name:        "command is split on whitespace",
			env:         map[string]string{"SVKEXE_UPDATE_COMMAND": "  /usr/local/bin/update.sh  --yes "},
			wantTrigger: DefaultTriggerPath,
			wantStatus:  DefaultStatusPath,
			wantCommand: []string{"/usr/local/bin/update.sh", "--yes"},
		},
		{
			name:        "blank command is ignored",
			env:         map[string]string{"SVKEXE_UPDATE_COMMAND": "   "},
			wantTrigger: DefaultTriggerPath,
			wantStatus:  DefaultStatusPath,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{"SVKEXE_UPDATE_TRIGGER", "SVKEXE_UPDATE_STATUS", "SVKEXE_UPDATE_COMMAND"} {
				t.Setenv(k, "")
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			cfg := NewRunnerFromEnv().Config()
			if cfg.TriggerPath != tc.wantTrigger {
				t.Errorf("TriggerPath = %q, want %q", cfg.TriggerPath, tc.wantTrigger)
			}
			if cfg.StatusPath != tc.wantStatus {
				t.Errorf("StatusPath = %q, want %q", cfg.StatusPath, tc.wantStatus)
			}
			if len(cfg.Command) != len(tc.wantCommand) {
				t.Fatalf("Command = %v, want %v", cfg.Command, tc.wantCommand)
			}
			for i := range cfg.Command {
				if cfg.Command[i] != tc.wantCommand[i] {
					t.Fatalf("Command = %v, want %v", cfg.Command, tc.wantCommand)
				}
			}
		})
	}
}

func TestWriteFileAtomicReplacesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := writeFileAtomic(path, []byte("new"), 0o644); err != nil {
		t.Fatalf("writeFileAtomic: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Errorf("content = %q, want %q", data, "new")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 0644", info.Mode().Perm())
	}
}

func TestWriteFileAtomicMissingDirectory(t *testing.T) {
	err := writeFileAtomic(filepath.Join(t.TempDir(), "nope", "file"), []byte("x"), 0o644)
	if err == nil {
		t.Fatal("writeFileAtomic succeeded for a missing directory")
	}
	if !strings.Contains(err.Error(), "create temp file") {
		t.Errorf("error = %q, want it wrapped with context", err)
	}
}
