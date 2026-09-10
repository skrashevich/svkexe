package updater

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The status file is the only channel between scripts/update.sh and the
// gateway: the script restarts the gateway halfway through, so the HTTP request
// that asked for the update is long gone by the time there is an outcome. This
// test runs the real script and decodes its output with the real RunState, so
// the two sides cannot drift apart — a shell-side change to the timestamp
// format or a key name fails here rather than silently in production.
func TestUpdateScriptStatusContract(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Fatalf("bash is required to verify the update script contract: %v", err)
	}

	// Run a copy from a scratch directory rather than the checkout: the script
	// derives its source tree from its own location, so a copy has no go.mod
	// above it and aborts at the source check. That failure is reached
	// identically as root and as an unprivileged user, which keeps the test
	// deterministic — and makes it impossible for the test to update the
	// machine it runs on.
	dir := t.TempDir()
	script := filepath.Join(dir, "update.sh")
	src, err := os.ReadFile(filepath.Join("..", "..", "scripts", "update.sh"))
	if err != nil {
		t.Fatalf("read update.sh: %v", err)
	}
	if err := os.WriteFile(script, src, 0o755); err != nil {
		t.Fatalf("write script copy: %v", err)
	}

	statusPath := filepath.Join(dir, "update-status.json")
	logPath := filepath.Join(dir, "update.log")

	cmd := exec.CommandContext(t.Context(), "bash", script)
	cmd.Env = append(os.Environ(),
		"SVKEXE_UPDATE_STATUS="+statusPath,
		"SVKEXE_UPDATE_LOG="+logPath,
		// A user that exists nowhere exercises the "service user missing"
		// branch of the ownership handling without touching real accounts.
		"SVKEXE_SERVICE_USER=svkexe-nonexistent-test-user",
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("script unexpectedly succeeded from a scratch directory; output:\n%s", out)
	}

	r := NewRunner(RunnerConfig{TriggerPath: filepath.Join(dir, "update.trigger"), StatusPath: statusPath})
	st, err := r.Status()
	if err != nil {
		t.Fatalf("Status: %v (script output:\n%s)", err, out)
	}

	if st.State != StateFailed {
		t.Errorf("State = %q, want %q (script output:\n%s)", st.State, StateFailed, out)
	}
	if !st.Terminal() {
		t.Errorf("Terminal() = false for state %q", st.State)
	}
	if st.Error == "" {
		t.Error("Error is empty; the script must record why it aborted")
	}
	if st.Log == "" {
		t.Error("Log is empty; the script must record a tail of its output")
	}
	// A zero timestamp means the shell emitted something time.Time could not
	// decode — the exact drift this test exists to catch.
	if st.StartedAt.IsZero() {
		t.Error("StartedAt did not decode as RFC3339")
	}
	if st.FinishedAt.IsZero() {
		t.Error("FinishedAt did not decode as RFC3339")
	}
	if !st.FinishedAt.IsZero() && st.FinishedAt.Before(st.StartedAt) {
		t.Errorf("FinishedAt %s precedes StartedAt %s", st.FinishedAt, st.StartedAt)
	}
	if time.Since(st.StartedAt) > time.Hour {
		t.Errorf("StartedAt %s is not from this run", st.StartedAt)
	}
}
