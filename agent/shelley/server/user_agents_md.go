package server

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// User-level AGENTS.md is versioned in a dedicated git repository so we can
// record a new commit every time the file is edited through the web (or iOS)
// UI. Two constraints shape the layout:
//
//  1. The file itself must stay at its historical path,
//     ~/.config/shelley/AGENTS.md, so nothing that already reads from there
//     (tooling, hooks, etc.) breaks.
//  2. ~/.config/shelley/ contains volatile state we must never accidentally
//     commit: shelley.db (~100 MB SQLite, churns constantly), shelley.db-wal,
//     unix sockets, per-terminal scratch dirs, etc.
//
// To satisfy both, the git directory lives separately under
// ~/.local/state/shelley/agents-md.git/ and points its work-tree at
// ~/.config/shelley/. The gitdir's info/exclude file ignores everything in
// that work-tree, with a single un-ignore for AGENTS.md, and we only ever
// `git add AGENTS.md` explicitly. That belt-and-suspenders setup means even a
// buggy future caller doing `git add -A` against this gitdir cannot snapshot
// the SQLite database or anything else in ~/.config/shelley.
//
// The git history is intentionally not exposed via the UI; users who need to
// recover older versions inspect the repo on disk.

const userAgentsMdFilename = "AGENTS.md"

// userAgentsMdGitDir returns the path of the gitdir that versions the
// user-level AGENTS.md. It is intentionally outside the work-tree it tracks.
func userAgentsMdGitDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "shelley", "agents-md.git"), nil
}

// userAgentsMdWorkTree returns the work-tree the AGENTS.md gitdir points at
// (i.e. the directory containing the file).
func userAgentsMdWorkTree() (string, error) {
	p, err := userAgentsMdPath()
	if err != nil {
		return "", err
	}
	return filepath.Dir(p), nil
}

// isUserAgentsMdFile reports whether the given absolute path is the
// user-level AGENTS.md (the only file we ever auto-commit).
func isUserAgentsMdFile(absPath string) bool {
	want, err := userAgentsMdPath()
	if err != nil {
		return false
	}
	return filepath.Clean(absPath) == filepath.Clean(want)
}

// ensureUserAgentsMdRepo creates the dedicated gitdir on first use and
// configures it so that the only path it can ever track is AGENTS.md.
func ensureUserAgentsMdRepo() error {
	gitDir, err := userAgentsMdGitDir()
	if err != nil {
		return err
	}
	workTree, err := userAgentsMdWorkTree()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(gitDir, "HEAD")); err == nil {
		return nil // already initialised
	}
	if err := os.MkdirAll(workTree, 0o755); err != nil {
		return fmt.Errorf("create AGENTS.md work-tree dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(gitDir), 0o755); err != nil {
		return fmt.Errorf("create AGENTS.md gitdir parent: %w", err)
	}
	// `git init --separate-git-dir` would also work but adds a .git file in
	// the work-tree; we don't want any new files in ~/.config/shelley.
	if err := runAgentsGit("init", "--quiet", "-b", "main", "--separate-git-dir="+gitDir, workTree); err != nil {
		// Older git lacks -b; try again.
		if err2 := runAgentsGit("init", "--quiet", "--separate-git-dir="+gitDir, workTree); err2 != nil {
			return fmt.Errorf("git init: %w", err)
		}
	}
	// `git init --separate-git-dir` writes a `.git` file in the work-tree
	// pointing at gitDir. We deliberately do NOT want that — it would mean
	// `git` invoked from anywhere in ~/.config/shelley starts thinking it's
	// in a repo. Remove it and instead point the gitdir at the work-tree via
	// core.worktree config so only invocations that pass --git-dir resolve.
	_ = os.Remove(filepath.Join(workTree, ".git"))
	if err := runAgentsGitIn(gitDir, "config", "core.worktree", workTree); err != nil {
		return fmt.Errorf("git config core.worktree: %w", err)
	}
	// Local identity so commits don't depend on global git config.
	_ = runAgentsGitIn(gitDir, "config", "user.email", "shelley@exe.dev")
	_ = runAgentsGitIn(gitDir, "config", "user.name", "Shelley")
	_ = runAgentsGitIn(gitDir, "config", "commit.gpgsign", "false")

	// Belt-and-suspenders: ignore everything in the work-tree except
	// AGENTS.md. We only ever `git add AGENTS.md` explicitly, but this
	// guarantees no future caller can ever snapshot shelley.db or anything
	// else in ~/.config/shelley through this gitdir.
	exclude := "/*\n!" + userAgentsMdFilename + "\n"
	if err := os.MkdirAll(filepath.Join(gitDir, "info"), 0o755); err != nil {
		return fmt.Errorf("create info dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "info", "exclude"), []byte(exclude), 0o644); err != nil {
		return fmt.Errorf("write info/exclude: %w", err)
	}

	// Seed an empty initial commit so the gitdir always has a HEAD to diff
	// against. Any pre-existing AGENTS.md content is recorded by the very
	// first real commitUserAgentsMd call, producing a clean baseline -> first
	// edit history.
	if err := runAgentsGitIn(gitDir, "commit", "--quiet", "--allow-empty", "-m", "initial"); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}

// commitUserAgentsMd ensures the gitdir exists, stages AGENTS.md, and records
// a commit if there is anything new. It is a no-op if AGENTS.md is unchanged.
// Callers should not treat failures as fatal: commit failure must not prevent
// the underlying file write from being reported as successful.
func commitUserAgentsMd(msg string) error {
	if err := ensureUserAgentsMdRepo(); err != nil {
		return err
	}
	gitDir, err := userAgentsMdGitDir()
	if err != nil {
		return err
	}
	// Stage AGENTS.md only — never `add -A`.
	if err := runAgentsGitIn(gitDir, "add", "--", userAgentsMdFilename); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	// Anything actually staged?
	cmd := exec.Command("git", "--git-dir="+gitDir, "diff", "--cached", "--quiet")
	cmd.Env = agentsGitEnv()
	if err := cmd.Run(); err == nil {
		return nil // nothing to commit
	}
	if err := runAgentsGitIn(gitDir, "commit", "--quiet", "-m", msg); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}

// runAgentsGit runs `git <args...>` with our hardened environment.
func runAgentsGit(args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Env = agentsGitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v: %w: %s", args, err, out)
	}
	return nil
}

// runAgentsGitIn runs `git --git-dir=<gitDir> <args...>` so the command
// resolves to our dedicated repo regardless of the process working directory.
func runAgentsGitIn(gitDir string, args ...string) error {
	full := append([]string{"--git-dir=" + gitDir}, args...)
	return runAgentsGit(full...)
}

func agentsGitEnv() []string {
	return append(
		os.Environ(),
		// Defend against weird global hooks/templates from a developer's machine.
		"GIT_TEMPLATE_DIR=",
		"GIT_OPTIONAL_LOCKS=0",
	)
}
