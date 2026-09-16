package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitLogCount(t *testing.T, gitDir string) int {
	t.Helper()
	out, err := exec.Command("git", "--git-dir="+gitDir, "rev-list", "--count", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-list: %v", err)
	}
	n := 0
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n)
	return n
}

// TestUserAgentsMdPathIsStable verifies that userAgentsMdPath is the
// historical ~/.config/shelley/AGENTS.md location and has no side effects.
func TestUserAgentsMdPathIsStable(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	p, err := userAgentsMdPath()
	if err != nil {
		t.Fatalf("userAgentsMdPath: %v", err)
	}
	want := filepath.Join(tmp, ".config", "shelley", "AGENTS.md")
	if p != want {
		t.Fatalf("path = %q, want %q", p, want)
	}
	// Merely asking for the path must not create the gitdir.
	gitDir, _ := userAgentsMdGitDir()
	if _, err := os.Stat(gitDir); !os.IsNotExist(err) {
		t.Fatalf("gitdir should not exist yet: %v", err)
	}
}

// TestHandleWriteFileAutoCommits verifies that POST /api/write-file to the
// user AGENTS.md path produces a new git commit in the dedicated gitdir,
// while leaving ~/.config/shelley/ otherwise untouched.
func TestHandleWriteFileAutoCommits(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	h := NewTestHarness(t)

	agentsPath, err := userAgentsMdPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(agentsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	// Drop a fake shelley.db next to AGENTS.md to make sure it never gets
	// committed.
	dbPath := filepath.Join(filepath.Dir(agentsPath), "shelley.db")
	if err := os.WriteFile(dbPath, []byte("PRETEND SQLITE"), 0o644); err != nil {
		t.Fatal(err)
	}

	write := func(path, content string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"path": path, "content": content})
		req := httptest.NewRequest(http.MethodPost, "/api/write-file", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.server.handleWriteFile(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("write %s: status %d: %s", path, w.Code, w.Body.String())
		}
	}

	write(agentsPath, "first edit\n")

	gitDir, _ := userAgentsMdGitDir()
	if _, err := os.Stat(gitDir); err != nil {
		t.Fatalf("gitdir should now exist: %v", err)
	}
	afterFirst := gitLogCount(t, gitDir)
	if afterFirst < 2 {
		t.Fatalf("expected >= 2 commits (initial + first edit); got %d", afterFirst)
	}

	write(agentsPath, "second edit\n")
	afterSecond := gitLogCount(t, gitDir)
	if afterSecond != afterFirst+1 {
		t.Fatalf("expected exactly one new commit; after first=%d after second=%d", afterFirst, afterSecond)
	}

	// Identical content should not create a new commit.
	write(agentsPath, "second edit\n")
	if got := gitLogCount(t, gitDir); got != afterSecond {
		t.Fatalf("identical write produced commit; got %d", got)
	}

	// Writes to other paths must not produce commits in our gitdir.
	other := filepath.Join(tmp, "other.txt")
	write(other, "x")
	if got := gitLogCount(t, gitDir); got != afterSecond {
		t.Fatalf("unrelated write produced commit; got %d", got)
	}

	// Even an explicit `git add -A` against this gitdir must not be able to
	// pick up shelley.db, thanks to info/exclude.
	cmd := exec.Command("git", "--git-dir="+gitDir, "add", "-A")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add -A: %v: %s", err, out)
	}
	lsOut, err := exec.Command("git", "--git-dir="+gitDir, "diff", "--cached", "--name-only").Output()
	if err != nil {
		t.Fatalf("git diff --cached: %v", err)
	}
	if strings.TrimSpace(string(lsOut)) != "" {
		t.Fatalf("git add -A staged unexpected files: %s", lsOut)
	}

	// The work-tree must not have a `.git` marker file that would make the
	// directory appear to be a repo to bystander git invocations.
	if _, err := os.Stat(filepath.Join(filepath.Dir(agentsPath), ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git in work-tree should not exist: %v", err)
	}
}

// TestHandleUserAgentsMdEndpoint checks the GET endpoint returns the file at
// the historical path.
func TestHandleUserAgentsMdEndpoint(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	h := NewTestHarness(t)

	agentsPath, _ := userAgentsMdPath()
	if err := os.MkdirAll(filepath.Dir(agentsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	want := "hello world\n"
	if err := os.WriteFile(agentsPath, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/user-agents-md", nil)
	w := httptest.NewRecorder()
	h.server.handleUserAgentsMd(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var resp struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Path != agentsPath {
		t.Fatalf("path = %q, want %q", resp.Path, agentsPath)
	}
	if resp.Content != want {
		t.Fatalf("content = %q, want %q", resp.Content, want)
	}
}

// TestPreexistingAgentsMdIsRecordedOnFirstCommit verifies that when the user
// already has an AGENTS.md from before the gitdir existed, the very first
// edit produces a diff against the empty seed commit (i.e. nothing is lost
// and history starts from the user's prior content).
func TestPreexistingAgentsMdIsRecordedOnFirstCommit(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	h := NewTestHarness(t)

	agentsPath, _ := userAgentsMdPath()
	if err := os.MkdirAll(filepath.Dir(agentsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	prior := "pre-existing content\n"
	if err := os.WriteFile(agentsPath, []byte(prior), 0o644); err != nil {
		t.Fatal(err)
	}

	// First write via the API: simulate a no-op "save" of the same content.
	body, _ := json.Marshal(map[string]string{"path": agentsPath, "content": prior})
	req := httptest.NewRequest(http.MethodPost, "/api/write-file", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.server.handleWriteFile(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	gitDir, _ := userAgentsMdGitDir()
	// The prior content should now live at HEAD.
	out, err := exec.Command("git", "--git-dir="+gitDir, "show", "HEAD:"+userAgentsMdFilename).Output()
	if err != nil {
		t.Fatalf("git show HEAD: %v", err)
	}
	if string(out) != prior {
		t.Fatalf("HEAD content = %q, want %q", out, prior)
	}
}
