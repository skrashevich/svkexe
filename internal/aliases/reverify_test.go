package aliases

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/dnscheck"
	"github.com/skrashevich/svkexe/internal/picoclaw"
	"github.com/skrashevich/svkexe/internal/runtime"
)

// scriptedVerifier answers from a fixed script so no test touches real DNS.
type scriptedVerifier struct {
	// result maps a hostname to what Verify should return for it.
	result map[string]error
	// asked is appended from the sweep goroutine; read it through askedCount
	// while a sweep may still be running.
	mu    sync.Mutex
	asked []string
}

func (s *scriptedVerifier) Verify(_ context.Context, hostname string) error {
	s.mu.Lock()
	s.asked = append(s.asked, hostname)
	s.mu.Unlock()
	return s.result[hostname]
}

func (s *scriptedVerifier) askedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.asked)
}

func reverifyFixture(t *testing.T) (*db.DB, *db.Container) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	owner, err := database.EnsureUser("owner", "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	c := &db.Container{ID: "vm", Name: "box", OwnerID: owner.ID, IncusName: "box", Status: "running"}
	if err := database.CreateContainer(c); err != nil {
		t.Fatal(err)
	}
	return database, c
}

func verifiedAlias(t *testing.T, database *db.DB, containerID, hostname string) *db.ContainerAlias {
	t.Helper()
	a, err := database.CreateContainerAlias(containerID, hostname)
	if err != nil {
		t.Fatalf("CreateContainerAlias(%s): %v", hostname, err)
	}
	if err := database.SetAliasVerification(a.ID, true, ""); err != nil {
		t.Fatalf("SetAliasVerification(%s): %v", hostname, err)
	}
	return a
}

// A domain whose owner repointed it must stop being served. Otherwise the row
// keeps answering both routing and the certificate check, and whoever owns the
// name next has their visitors land in somebody else's VM.
func TestReverifyStopsRoutingARepointedDomain(t *testing.T) {
	database, c := reverifyFixture(t)
	moved := verifiedAlias(t, database, c.ID, "moved.example.org")
	stays := verifiedAlias(t, database, c.ID, "stays.example.org")

	verifier := &scriptedVerifier{result: map[string]error{
		"moved.example.org": fmt.Errorf("%w: resolves to 203.0.113.9", dnscheck.ErrPointsElsewhere),
		"stays.example.org": nil,
	}}
	reverifyOnce(t.Context(), database, nil, verifier)

	got, err := database.GetAliasByID(moved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Verified {
		t.Error("a domain that now points elsewhere is still routed")
	}
	if got.LastError == "" {
		t.Error("the owner is not told why their domain stopped working")
	}
	if _, err := database.GetVerifiedAliasByHostname("moved.example.org"); err == nil {
		t.Error("the repointed hostname still resolves")
	}

	if got, err := database.GetAliasByID(stays.ID); err != nil || !got.Verified {
		t.Errorf("a domain that still points here was demoted: %+v, %v", got, err)
	}
}

// The one thing a re-check must never do is take working domains offline
// because DNS was briefly unavailable — and a gateway that cannot resolve its
// own address fails every alias at once, so this is a batch failure mode.
func TestReverifyIgnoresInconclusiveFailures(t *testing.T) {
	database, c := reverifyFixture(t)
	first := verifiedAlias(t, database, c.ID, "one.example.org")
	second := verifiedAlias(t, database, c.ID, "two.example.org")

	verifier := &scriptedVerifier{result: map[string]error{
		"one.example.org": errors.New("one.example.org does not resolve yet (DNS may still be propagating)"),
		"two.example.org": errors.New("cannot verify aliases because this gateway does not know its own address"),
	}}
	reverifyOnce(t.Context(), database, nil, verifier)

	for _, a := range []*db.ContainerAlias{first, second} {
		got, err := database.GetAliasByID(a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Verified {
			t.Errorf("%s was taken out of service over an inconclusive check", a.Hostname)
		}
	}
}

// Only routed domains are worth re-checking; a pending one has nothing to lose.
func TestReverifySkipsUnverifiedAliases(t *testing.T) {
	database, c := reverifyFixture(t)
	if _, err := database.CreateContainerAlias(c.ID, "pending.example.org"); err != nil {
		t.Fatal(err)
	}
	verifiedAlias(t, database, c.ID, "live.example.org")

	verifier := &scriptedVerifier{result: map[string]error{"live.example.org": nil}}
	reverifyOnce(t.Context(), database, nil, verifier)

	if len(verifier.asked) != 1 || verifier.asked[0] != "live.example.org" {
		t.Errorf("re-check looked at %v, want only the routed domain", verifier.asked)
	}
}

// A cancelled context stops the sweep rather than working through the rest.
func TestReverifyStopsOnContextCancel(t *testing.T) {
	database, c := reverifyFixture(t)
	verifiedAlias(t, database, c.ID, "a.example.org")
	verifiedAlias(t, database, c.ID, "b.example.org")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	verifier := &scriptedVerifier{result: map[string]error{}}
	reverifyOnce(ctx, database, nil, verifier)

	if len(verifier.asked) != 0 {
		t.Errorf("re-check kept going after cancellation: %v", verifier.asked)
	}
}

// Without a verifier there is nothing to re-check, and the loop must not spin.
func TestReverifyWithoutAVerifierReturns(t *testing.T) {
	database, _ := reverifyFixture(t)
	done := make(chan struct{})
	go func() {
		Reverify(t.Context(), database, nil, nil, ReverifyInterval)
		close(done)
	}()
	<-done
}

// guestRuntime records what the sweep does to a VM, so a test can tell whether
// the agent guide was rewritten. Only Exec matters here; the rest satisfies the
// interface.
type guestRuntime struct {
	commands [][]string
}

func (g *guestRuntime) Create(context.Context, runtime.CreateOpts) (*runtime.Container, error) {
	return &runtime.Container{}, nil
}
func (g *guestRuntime) Start(context.Context, string) error  { return nil }
func (g *guestRuntime) Stop(context.Context, string) error   { return nil }
func (g *guestRuntime) Delete(context.Context, string) error { return nil }
func (g *guestRuntime) Get(context.Context, string) (*runtime.Container, error) {
	return &runtime.Container{}, nil
}
func (g *guestRuntime) List(context.Context, string) ([]*runtime.Container, error) { return nil, nil }
func (g *guestRuntime) Exec(_ context.Context, _ string, cmd []string) ([]byte, error) {
	g.commands = append(g.commands, cmd)
	return nil, nil
}
func (g *guestRuntime) Snapshot(context.Context, string, string) error { return nil }
func (g *guestRuntime) SetNesting(context.Context, string, bool) error { return nil }

// The agent was told this domain serves its work. Leaving it in the guide after
// the sweep stops routing it would have the agent keep reporting a URL that now
// answers 404.
func TestReverifyRewritesTheAgentGuideOnDemote(t *testing.T) {
	database, c := reverifyFixture(t)
	verifiedAlias(t, database, c.ID, "moved.example.org")

	guest := &guestRuntime{}
	verifier := &scriptedVerifier{result: map[string]error{
		"moved.example.org": fmt.Errorf("%w: resolves to 203.0.113.9", dnscheck.ErrPointsElsewhere),
	}}
	reverifyOnce(t.Context(), database, guest, verifier)

	var rewritten bool
	for _, cmd := range guest.commands {
		for _, arg := range cmd {
			if strings.Contains(arg, picoclaw.GuideFilePath) {
				rewritten = true
			}
		}
	}
	if !rewritten {
		t.Errorf("the agent guide was not rewritten after the domain stopped being routed: %v", guest.commands)
	}
}

// A domain that still points here must not cause any VM to be touched.
func TestReverifyLeavesHealthyVMsAlone(t *testing.T) {
	database, c := reverifyFixture(t)
	verifiedAlias(t, database, c.ID, "stays.example.org")

	guest := &guestRuntime{}
	verifier := &scriptedVerifier{result: map[string]error{"stays.example.org": nil}}
	reverifyOnce(t.Context(), database, guest, verifier)

	if len(guest.commands) != 0 {
		t.Errorf("the sweep talked to a VM whose domains are all fine: %v", guest.commands)
	}
}

// A ticker alone would mean a gateway that restarts more often than the
// interval never sweeps at all, and this one updates and restarts itself.
func TestReverifySweepsOnStartup(t *testing.T) {
	database, c := reverifyFixture(t)
	verifiedAlias(t, database, c.ID, "live.example.org")

	original := ReverifyStartupJitter
	ReverifyStartupJitter = 0
	t.Cleanup(func() { ReverifyStartupJitter = original })

	ctx, cancel := context.WithCancel(t.Context())
	verifier := &scriptedVerifier{result: map[string]error{"live.example.org": nil}}
	done := make(chan struct{})
	go func() {
		// An interval far longer than the test: only a startup sweep can run.
		Reverify(ctx, database, nil, verifier, time.Hour)
		close(done)
	}()

	deadline := time.After(5 * time.Second)
	for verifier.askedCount() == 0 {
		select {
		case <-deadline:
			cancel()
			t.Fatal("no sweep ran at startup; the first one would wait a whole interval")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
}
