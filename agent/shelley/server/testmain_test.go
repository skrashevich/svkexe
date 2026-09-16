package server

import (
	"errors"
	"net/http"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	exeDevDefaultPortHTTPClient = &http.Client{Transport: defaultPortTestTransport{}}
	// Default to a failing reflection client so server tests never make real
	// network calls. Tests that exercise reflection override this explicitly.
	setReflectionHTTPClient(&http.Client{Transport: defaultPortTestTransport{}})
	// Isolate from any host-wide git config (e.g. core.hooksPath that
	// enforces commit-message policy on agent-driven commits) so tests
	// that invoke `git commit` behave deterministically.
	os.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	os.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	// Run from an empty temp dir, not the package dir. Conversations created
	// with cwd="" fall back to os.Getwd(), and system-prompt generation then
	// walks the whole surrounding git repo (guidance-file + skills walks).
	// Inside a large monorepo checkout that is millions of stat calls across
	// the suite; each one is also recorded in go test's testlog, which `go
	// test` re-hashes and writes to the build cache after the binary exits
	// (observed: a 500MB testlog adding 2+ minutes of dead time in CI after
	// the last test finished). An empty cwd keeps those walks trivially
	// small without changing what the tests exercise; tests that care about
	// specific trees pass an explicit cwd already.
	tmp, err := os.MkdirTemp("", "shelley-server-test-cwd-")
	if err != nil {
		panic(err)
	}
	if err := os.Chdir(tmp); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

type defaultPortTestTransport struct{}

func (defaultPortTestTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("reflection disabled in tests")
}
