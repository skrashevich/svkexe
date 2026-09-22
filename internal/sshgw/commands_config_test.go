package sshgw

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
)

// configRuntime records the instance settings the config commands write. Only
// the two methods these paths reach are implemented; the embedded nil interface
// turns an accidental call to any other into a panic rather than a silent pass.
type configRuntime struct {
	runtime.ContainerRuntime
	nesting map[string]bool
}

func newConfigRuntime() *configRuntime { return &configRuntime{nesting: map[string]bool{}} }

func (r *configRuntime) SetNesting(_ context.Context, id string, enabled bool) error {
	r.nesting[id] = enabled
	return nil
}

// Exec answers the agent-guide refresh that follows a publish or a nesting
// change. It writes a file inside the VM and reads nothing back, so an empty
// reply is the whole of a successful one.
func (r *configRuntime) Exec(context.Context, string, []string) ([]byte, error) {
	return nil, nil
}

// configFixture builds a gateway with one owner and one running VM.
func configFixture(t *testing.T) (*Server, *db.User, *db.Container) {
	t.Helper()
	return configFixtureWithTask(t, "")
}

// configFixtureWithTask builds the same gateway with a task queued on the VM.
// The task has to be set at creation: asking for one is what queues it, and
// nothing else puts a VM into a state where a task can be retried.
func configFixtureWithTask(t *testing.T, task string) (*Server, *db.User, *db.Container) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	user := &db.User{ID: "u", Email: "u@example.test", Role: "user"}
	if err := database.CreateUser(user); err != nil {
		t.Fatal(err)
	}
	container := &db.Container{
		ID: "vm", Name: "dev", IncusName: "incus-dev", OwnerID: user.ID, Status: "running", InitialTask: task,
	}
	if err := database.CreateContainer(container); err != nil {
		t.Fatal(err)
	}
	return &Server{db: database, runtime: newConfigRuntime(), domain: "example.test"}, user, container
}

func TestSSHPublishStoresPortAndVisibility(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		wantPort   int
		wantPublic bool
	}{
		{"port_only_keeps_visibility", "publish dev 8080", 8080, false},
		{"port_and_public", "publish dev 8080 public", 8080, true},
		{"port_and_private", "publish dev 8080 private", 8080, false},
		{"visibility_alone_keeps_port", "publish dev public", db.DefaultAppPort, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, user, container := configFixture(t)
			if _, err := run(t, s, user, tc.line); err != nil {
				t.Fatalf("%s: %v", tc.line, err)
			}
			got, err := s.db.GetContainerByID(container.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.AppPort != tc.wantPort || got.AppPublic != tc.wantPublic {
				t.Errorf("stored port=%d public=%v, want port=%d public=%v", got.AppPort, got.AppPublic, tc.wantPort, tc.wantPublic)
			}
		})
	}
}

// The agent port is the one port that must never be published: serving it would
// hand command execution inside the VM to anonymous callers.
func TestSSHPublishRejectsInvalidPort(t *testing.T) {
	for _, line := range []string{
		"publish dev 0",
		"publish dev 70000",
		"publish dev 9000",
		"publish dev http",
	} {
		t.Run(line, func(t *testing.T) {
			s, user, container := configFixture(t)
			if _, err := run(t, s, user, line); err == nil {
				t.Fatalf("%s was accepted", line)
			}
			got, err := s.db.GetContainerByID(container.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.AppPort != db.DefaultAppPort {
				t.Errorf("a refused port still changed the VM: port=%d", got.AppPort)
			}
		})
	}
}

func TestSSHPublishReportsWithoutChangingAnything(t *testing.T) {
	s, user, container := configFixture(t)
	if err := s.db.UpdateContainerPublish(container.ID, 8080, true); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, s, user, "publish dev")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "8080") || !strings.Contains(out, "public") {
		t.Errorf("publish did not report the current setting: %q", out)
	}

	out, err = run(t, s, user, "publish dev --json")
	if err != nil {
		t.Fatal(err)
	}
	var view vmJSON
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if view.AppPort != 8080 || !view.AppPublic {
		t.Errorf("JSON view = %+v, want port 8080 public", view)
	}
}

// The owner decides nesting for their own VM, but the deployment-wide ceiling is
// the operator's and cannot be lifted from a tenant session.
func TestSSHNestingRefusesToTurnOnWhenTheDeploymentForbidsIt(t *testing.T) {
	s, user, container := configFixture(t)
	if err := s.db.SetNestingAllowed(false); err != nil {
		t.Fatal(err)
	}
	if err := s.db.UpdateContainerNesting(container.ID, false); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, s, user, "nesting dev on")
	if err == nil {
		t.Fatalf("the ban was ignored: %q", out)
	}
	if !strings.Contains(err.Error(), "disabled for this deployment") {
		t.Errorf("unhelpful refusal: %v", err)
	}
	got, err := s.db.GetContainerByID(container.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Nesting {
		t.Error("the refused wish was stored anyway")
	}

	// Turning it off is always allowed: a ban is a ceiling, not a floor.
	if _, err := run(t, s, user, "nesting dev off"); err != nil {
		t.Fatalf("nesting off under a ban: %v", err)
	}
}

func TestSSHNestingStoresTheWishAndAsksForARestart(t *testing.T) {
	s, user, container := configFixture(t)
	if err := s.db.UpdateContainerNesting(container.ID, false); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, s, user, "nesting dev on")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.db.GetContainerByID(container.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Nesting {
		t.Fatal("the owner's wish was not stored")
	}
	// LXC reads security.nesting at boot, so the running VM still owes a restart
	// and the command has to say so rather than report the change as live.
	if got.NestingApplied {
		t.Error("a running VM was recorded as having booted with the new setting")
	}
	if !strings.Contains(out, "restart dev") {
		t.Errorf("the owner was not told a restart is owed: %q", out)
	}
	rt := s.runtime.(*configRuntime)
	if !rt.nesting[container.IncusName] {
		t.Error("the setting never reached the instance")
	}
}

func TestSSHNestingReportsTheCurrentState(t *testing.T) {
	s, user, container := configFixture(t)
	if err := s.db.UpdateContainerNesting(container.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.db.SetNestingApplied(container.ID, true); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, s, user, "nesting dev")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "on") || strings.Contains(out, "pending") {
		t.Errorf("state report = %q, want a settled \"on\"", out)
	}
}

// Retrying re-arms a failed task: it goes back to pending so that the next
// delivery picks it up, and the recorded failure is cleared.
func TestSSHTaskRetryReArmsTheTask(t *testing.T) {
	s, user, container := configFixtureWithTask(t, "set up a Go project")
	if err := s.db.SetInitialTaskState(container.ID, db.TaskFailed, "the agent did not accept the task"); err != nil {
		t.Fatal(err)
	}

	// With no runtime there is nothing to deliver to, which is exactly the case
	// that has to leave the task pending rather than burning the retry.
	s.runtime = nil
	if _, err := run(t, s, user, "task retry dev"); err != nil {
		t.Fatal(err)
	}
	got, err := s.db.GetContainerByID(container.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.InitialTaskState != db.TaskPending || got.InitialTaskError != "" {
		t.Errorf("state=%q error=%q, want %q and no error", got.InitialTaskState, got.InitialTaskError, db.TaskPending)
	}
}

func TestSSHTaskRetryRefusesATaskThatDidNotFail(t *testing.T) {
	s, user, _ := configFixtureWithTask(t, "set up a Go project")
	if _, err := run(t, s, user, "task retry dev"); err == nil {
		t.Fatal("a pending task was retried")
	}
}

func TestSSHTaskReportsState(t *testing.T) {
	s, user, container := configFixtureWithTask(t, "set up a Go project")
	if err := s.db.SetInitialTaskState(container.ID, db.TaskFailed, "no model configured"); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, s, user, "task dev --json")
	if err != nil {
		t.Fatal(err)
	}
	var view taskJSON
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if view.State != db.TaskFailed || view.Error != "no model configured" || view.Text != "set up a Go project" {
		t.Errorf("JSON view = %+v", view)
	}

	if out, err = run(t, s, user, "task dev"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no model configured") {
		t.Errorf("the failure was not reported: %q", out)
	}
}

func TestSSHAgentNeedsARunningVM(t *testing.T) {
	s, user, container := configFixture(t)
	if err := s.db.UpdateContainerStatus(container.ID, "stopped", ""); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"agent dev", "agent update dev"} {
		_, err := run(t, s, user, line)
		if err == nil || !strings.Contains(err.Error(), "not running") {
			t.Errorf("%s on a stopped VM: %v", line, err)
		}
	}
}

func TestSSHShareCreatesALinkAtTheWorkloadHost(t *testing.T) {
	s, user, container := configFixture(t)

	out, err := run(t, s, user, "share dev")
	if err != nil {
		t.Fatal(err)
	}
	links, err := s.db.ListSharedLinksByContainer(container.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("got %d links, want 1", len(links))
	}
	if links[0].ExpiresAt != nil {
		t.Error("a link created without --ttl was given an expiry")
	}
	// The URL is the whole point of the command: an owner pastes it somewhere.
	want := "https://dev.example.test/?share=" + links[0].Token
	if !strings.Contains(out, want) {
		t.Errorf("output %q does not contain %q", out, want)
	}
}

func TestSSHShareTTLSetsAnExpiry(t *testing.T) {
	s, user, container := configFixture(t)

	if _, err := run(t, s, user, "share dev --ttl=24h"); err != nil {
		t.Fatal(err)
	}
	links, err := s.db.ListSharedLinksByContainer(container.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].ExpiresAt == nil {
		t.Fatalf("--ttl did not set an expiry: %+v", links)
	}

	if _, err := run(t, s, user, "share dev --ttl=yesterday"); err == nil {
		t.Error("an unparsable --ttl was accepted")
	}
}

func TestSSHShareListRendersEveryLink(t *testing.T) {
	s, user, container := configFixture(t)
	first, err := s.db.CreateSharedLink(container.ID, user.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.db.CreateSharedLink(container.ID, user.ID, nil)
	if err != nil {
		t.Fatal(err)
	}

	out, err := run(t, s, user, "share list dev --json")
	if err != nil {
		t.Fatal(err)
	}
	var views []sharedLinkJSON
	if err := json.Unmarshal([]byte(out), &views); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if len(views) != 2 {
		t.Fatalf("got %d links, want 2", len(views))
	}
	seen := map[string]string{}
	for _, v := range views {
		seen[v.Token] = v.URL
	}
	for _, token := range []string{first.Token, second.Token} {
		if seen[token] != "https://dev.example.test/?share="+token {
			t.Errorf("link %s rendered as %q", token, seen[token])
		}
	}
}

// A share token is a bare credential, so revoking is the one command that does
// not start from an owner-scoped lookup. It has to do the ownership check
// itself, and answer a stranger's token exactly as it answers an unknown one.
func TestSSHShareRevokeRefusesAnotherOwnersToken(t *testing.T) {
	s, user, _ := configFixture(t)
	stranger := &db.User{ID: "other", Email: "other@example.test", Role: "user"}
	if err := s.db.CreateUser(stranger); err != nil {
		t.Fatal(err)
	}
	if err := s.db.CreateContainer(&db.Container{
		ID: "vm2", Name: "theirs", IncusName: "incus-theirs", OwnerID: stranger.ID, Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	theirs, err := s.db.CreateSharedLink("vm2", stranger.ID, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = run(t, s, user, "share revoke "+theirs.Token)
	if err == nil {
		t.Fatal("a stranger's share link was revoked")
	}
	// The refusal must not reveal that the token exists, so it reads the same as
	// the answer to a token nobody ever issued.
	_, unknownErr := run(t, s, user, "share revoke not-a-real-token")
	if unknownErr == nil || err.Error() != unknownErr.Error() {
		t.Errorf("a stranger's token answers %v, an unknown one %v", err, unknownErr)
	}
	if _, err := s.db.LookupSharedLink(theirs.Token); err != nil {
		t.Errorf("the stranger's link was deleted anyway: %v", err)
	}
}

func TestSSHShareRevokeDeletesAnOwnedToken(t *testing.T) {
	s, user, container := configFixture(t)
	link, err := s.db.CreateSharedLink(container.ID, user.ID, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := run(t, s, user, "share revoke "+link.Token); err != nil {
		t.Fatal(err)
	}
	links, err := s.db.ListSharedLinksByContainer(container.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Errorf("the link survived its revocation: %+v", links)
	}
}

func TestSSHURLListsEveryAddress(t *testing.T) {
	s, user, container := configFixture(t)

	out, err := run(t, s, user, "url dev --json")
	if err != nil {
		t.Fatal(err)
	}
	var view urlsJSON
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	want := urlsJSON{
		Workload:  "https://dev.example.test",
		Agent:     "https://agent-dev.example.test",
		Dashboard: "https://example.test/dashboard/vms",
		Shell:     "https://example.test/dashboard/vms/" + container.ID + "/shell",
	}
	if !reflect.DeepEqual(view, want) {
		t.Errorf("view = %+v, want %+v", view, want)
	}
}

// A deployment without a domain serves no addresses at all, and saying so is
// more useful than printing "https://dev./".
func TestSSHURLSaysSoWithoutADomain(t *testing.T) {
	s, user, _ := configFixture(t)
	s.domain = ""

	out, err := run(t, s, user, "url dev")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no domain configured") {
		t.Errorf("output = %q", out)
	}
}

// Every command in this group is owner-scoped, so another account's VM is not
// refused, it simply does not exist.
func TestSSHConfigCommandsAreOwnerScoped(t *testing.T) {
	s, _, _ := configFixture(t)
	stranger := &db.User{ID: "other", Email: "other@example.test", Role: "user"}
	if err := s.db.CreateUser(stranger); err != nil {
		t.Fatal(err)
	}

	for _, line := range []string{
		"publish dev 8080",
		"nesting dev on",
		"task dev",
		"task retry dev",
		"agent dev",
		"share dev",
		"share list dev",
		"url dev",
	} {
		_, err := run(t, s, stranger, line)
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Errorf("%q from a stranger: %v", line, err)
		}
	}
}

// agentRuntime answers the probes the agent commands make inside a VM: the
// checksum of the running executable, the version handshake of an artifact
// before it replaces it, and the readiness curl afterwards.
type agentRuntime struct {
	runtime.ContainerRuntime
	runningSum string
	steps      []string
	pushed     map[string][]byte
}

func newAgentRuntime(runningSum string) *agentRuntime {
	return &agentRuntime{runningSum: runningSum, pushed: map[string][]byte{}}
}

func (r *agentRuntime) PushFile(_ context.Context, _, path string, data []byte) error {
	r.pushed[path] = data
	r.steps = append(r.steps, "push "+path)
	return nil
}

// PullFile is here only so the fake satisfies runtime.FileRuntime, which is
// what gates the install path.
func (r *agentRuntime) PullFile(context.Context, string, string) ([]byte, error) {
	return nil, nil
}

func (r *agentRuntime) Exec(_ context.Context, _ string, cmd []string) ([]byte, error) {
	text := strings.Join(cmd, " ")
	r.steps = append(r.steps, text)
	switch {
	case strings.Contains(text, "sha256sum"):
		return []byte(r.runningSum + "  /proc/1/exe\n"), nil
	case len(cmd) == 2 && cmd[1] == "version":
		return []byte(`{"version":"picoclaw-v0.3.1-svkexe","customized":true}`), nil
	default:
		return nil, nil
	}
}

// The gateway ships the agent build its VMs are meant to run, so "agent update"
// has to actually install that artifact and bring the service back, not merely
// report a difference.
func TestSSHAgentUpdateInstallsTheGatewayBuild(t *testing.T) {
	artifact := filepath.Join(t.TempDir(), "picoclaw")
	if err := os.WriteFile(artifact, []byte("the gateway's agent build"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SVKEXE_AGENT_BINARY", artifact)
	wanted := fmt.Sprintf("%x", sha256.Sum256([]byte("the gateway's agent build")))

	s, user, _ := configFixture(t)
	// The VM reports a different build, which is what an available update is.
	rt := newAgentRuntime(strings.Repeat("a", 64))
	s.runtime = rt

	report, err := run(t, s, user, "agent dev")
	if err != nil {
		t.Fatalf("agent dev: %v", err)
	}
	if !strings.Contains(report, wanted[:12]) && !strings.Contains(strings.ToLower(report), "update") {
		t.Fatalf("the report does not mention the available update:\n%s", report)
	}

	if _, err := run(t, s, user, "agent update dev"); err != nil {
		t.Fatalf("agent update dev: %v", err)
	}
	if got := string(rt.pushed["/usr/local/bin/picoclaw.new"]); got != "the gateway's agent build" {
		t.Fatalf("the gateway artifact was not installed, pushed %q", got)
	}
	steps := strings.Join(rt.steps, "\n")
	install, restart := strings.Index(steps, "mv -f /usr/local/bin/picoclaw.new"), strings.Index(steps, "restart picoclaw.service")
	if install < 0 || restart < 0 || install > restart {
		t.Fatalf("the new build was not put in place before the restart:\n%s", steps)
	}
	if !strings.Contains(steps, "curl") {
		t.Errorf("the update reported success without waiting for the agent to answer:\n%s", steps)
	}
}
