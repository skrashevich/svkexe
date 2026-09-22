package sshgw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	gssh "github.com/gliderlabs/ssh"
	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
)

// fakeContext is the session context handleSession reads the authenticated user
// out of. Only the value lookup and the context methods are exercised.
type fakeContext struct {
	gssh.Context
	ctx    context.Context
	values map[any]any
}

func (f *fakeContext) Deadline() (time.Time, bool) { return f.ctx.Deadline() }
func (f *fakeContext) Done() <-chan struct{}       { return f.ctx.Done() }
func (f *fakeContext) Err() error                  { return f.ctx.Err() }
func (f *fakeContext) Value(key any) any {
	if v, ok := f.values[key]; ok {
		return v
	}
	return f.ctx.Value(key)
}

// fakeSession stands in for a live SSH session: it carries the login, the
// command line, the input the client would have typed, and records what was
// written back and with which exit status.
type fakeSession struct {
	gssh.Session
	ctx      *fakeContext
	login    string
	command  string
	input    io.Reader
	output   bytes.Buffer
	errs     bytes.Buffer
	exitCode int
	exited   bool
}

func newSession(t *testing.T, user *db.User, login, command, input string) *fakeSession {
	t.Helper()
	return &fakeSession{
		ctx:     &fakeContext{ctx: t.Context(), values: map[any]any{ctxUser{}: user}},
		login:   login,
		command: command,
		input:   strings.NewReader(input),
	}
}

func (s *fakeSession) User() string          { return s.login }
func (s *fakeSession) RawCommand() string    { return s.command }
func (s *fakeSession) Command() []string     { return strings.Fields(s.command) }
func (s *fakeSession) Context() gssh.Context { return s.ctx }
func (s *fakeSession) Pty() (gssh.Pty, <-chan gssh.Window, bool) {
	return gssh.Pty{}, nil, false
}
func (s *fakeSession) Read(p []byte) (int, error)  { return s.input.Read(p) }
func (s *fakeSession) Write(p []byte) (int, error) { return s.output.Write(p) }
func (s *fakeSession) Stderr() io.ReadWriter       { return &s.errs }
func (s *fakeSession) Exit(code int) error {
	s.exitCode = code
	s.exited = true
	return nil
}

// attachRuntime records the interactive exec a direct VM login performs.
type attachRuntime struct {
	runtime.ContainerRuntime
	attached runtime.ExecInteractiveOpts
	calls    int
}

func (r *attachRuntime) ExecInteractive(_ context.Context, opts runtime.ExecInteractiveOpts) error {
	r.calls++
	r.attached = opts
	close(opts.Done)
	return nil
}

func newTestServer(t *testing.T, rt runtime.ContainerRuntime) (*Server, *db.DB) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return &Server{db: database, runtime: rt, domain: "example.test"}, database
}

func newTestUser(t *testing.T, database *db.DB, id, email, role string) *db.User {
	t.Helper()
	user := &db.User{ID: id, Email: email, Role: role}
	if err := database.CreateUser(user); err != nil {
		t.Fatal(err)
	}
	return user
}

// The SSH key is what identifies the user; the login is only a shortcut. Any
// login that does not name one of their VMs therefore has to land on the menu,
// because a client picks the local username by default and refusing it would
// lock out everyone whose account name is not also a VM name.
func TestUnknownLoginReachesTheMenu(t *testing.T) {
	s, database := newTestServer(t, nil)
	user := newTestUser(t, database, "u1", "u@example.test", "user")

	sess := newSession(t, user, "svk", "", "")
	s.handleSession(sess)

	output := sess.output.String()
	if !strings.Contains(output, "help --json") {
		t.Fatalf("an unknown login did not reach the menu: %q", output)
	}
	if strings.Contains(sess.errs.String(), "not found") {
		t.Fatalf("an unknown login was refused: %q", sess.errs.String())
	}
}

// A login naming one of the caller's own VMs keeps its old meaning: connect me
// to that VM.
func TestVMNameLoginConnectsDirectly(t *testing.T) {
	rt := &attachRuntime{}
	s, database := newTestServer(t, rt)
	user := newTestUser(t, database, "u1", "u@example.test", "user")
	if err := database.CreateContainer(&db.Container{
		ID: "vm", Name: "dev", IncusName: "svkexe-u1-dev", OwnerID: user.ID, Status: "running",
	}); err != nil {
		t.Fatal(err)
	}

	for _, login := range []string{"dev", "svkexe-u1-dev"} {
		rt.calls = 0
		sess := newSession(t, user, login, "", "")
		s.handleSession(sess)
		if rt.calls != 1 || rt.attached.IncusName != "svkexe-u1-dev" {
			t.Fatalf("login %q did not attach to the VM: %d calls, %q", login, rt.calls, rt.attached.IncusName)
		}
		if sess.exitCode != 0 {
			t.Fatalf("login %q exited with %d: %s", login, sess.exitCode, sess.errs.String())
		}
	}
}

// A VM named after the caller's own login would otherwise swallow every
// attempt to reach the menu, so one login stays reserved for it.
func TestReservedLoginAlwaysOpensTheMenu(t *testing.T) {
	rt := &attachRuntime{}
	s, database := newTestServer(t, rt)
	user := newTestUser(t, database, "u1", "u@example.test", "user")
	if err := database.CreateContainer(&db.Container{
		ID: "vm", Name: menuLogin, IncusName: "svkexe-u1-" + menuLogin, OwnerID: user.ID, Status: "running",
	}); err != nil {
		t.Fatal(err)
	}

	sess := newSession(t, user, menuLogin, "ls", "")
	s.handleSession(sess)

	if rt.calls != 0 {
		t.Fatalf("the reserved login attached to a VM instead of opening the menu")
	}
	if !strings.Contains(sess.output.String(), menuLogin) || sess.exitCode != 0 {
		t.Fatalf("the reserved login did not run the menu command: %q (exit %d)", sess.output.String(), sess.exitCode)
	}
}

// A command given on the ssh command line runs inside the VM the login names,
// which is what any other SSH host does.
func TestVMNameLoginRunsTheGivenCommandInsideTheVM(t *testing.T) {
	rt := &attachRuntime{}
	s, database := newTestServer(t, rt)
	user := newTestUser(t, database, "u1", "u@example.test", "user")
	if err := database.CreateContainer(&db.Container{
		ID: "vm", Name: "dev", IncusName: "svkexe-u1-dev", OwnerID: user.ID, Status: "running",
	}); err != nil {
		t.Fatal(err)
	}

	sess := newSession(t, user, "dev", "uname -a", "")
	s.handleSession(sess)

	if got := strings.Join(rt.attached.Command, " "); !strings.Contains(got, "uname -a") {
		t.Fatalf("the command did not reach the VM: %q", got)
	}
}

// One command per connection is how a script or an LLM agent drives the
// gateway, so the status it exits with has to mean something.
func TestOneShotCommandExitStatus(t *testing.T) {
	s, database := newTestServer(t, nil)
	user := newTestUser(t, database, "u1", "u@example.test", "user")

	cases := []struct {
		name    string
		command string
		want    int
		inOut   string
	}{
		{name: "success", command: "ls", want: 0, inOut: "No VMs found"},
		{name: "failure", command: "stat nope", want: exitFailure},
		{name: "unknown command", command: "definitely-not-a-command", want: exitUnknownCommand},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := newSession(t, user, "anything", tc.command, "")
			s.handleSession(sess)

			if !sess.exited || sess.exitCode != tc.want {
				t.Fatalf("exit status %d, want %d (stderr: %s)", sess.exitCode, tc.want, sess.errs.String())
			}
			if tc.inOut != "" && !strings.Contains(sess.output.String(), tc.inOut) {
				t.Fatalf("output %q does not contain %q", sess.output.String(), tc.inOut)
			}
			if strings.Contains(sess.output.String(), "svk ▶") || strings.Contains(sess.output.String(), "_____") {
				t.Fatalf("a one-shot invocation printed the interactive banner or prompt: %q", sess.output.String())
			}
		})
	}
}

// A session that never asked for a terminal is a script or an agent. What it
// needs first is the command that describes every other command.
func TestSessionWithoutPTYIsGreetedWithAnAgentBrief(t *testing.T) {
	s, database := newTestServer(t, nil)
	user := newTestUser(t, database, "u1", "agent@example.test", "user")

	sess := newSession(t, user, "svk", "", "exit\n")
	s.handleSession(sess)

	output := sess.output.String()
	for _, want := range []string{"agent@example.test", "help --json", "example.test"} {
		if !strings.Contains(output, want) {
			t.Fatalf("the brief does not mention %q: %q", want, output)
		}
	}
	if strings.Contains(output, "_____") {
		t.Fatal("a session without a terminal was sent the ASCII banner")
	}
}

// The registry is the single declaration of a command. A command that is not
// fully declared would be undocumented, ungrouped or unreachable, and none of
// those can be noticed by reading the dispatch code.
func TestRegistryIsComplete(t *testing.T) {
	groups := map[string]bool{}
	for _, g := range groupOrder {
		groups[g] = true
	}
	for _, cmd := range registry {
		switch {
		case cmd.Name == "":
			t.Fatal("a command has no name")
		case cmd.Usage == "":
			t.Errorf("%s: no usage", cmd.Name)
		case cmd.Summary == "":
			t.Errorf("%s: no summary", cmd.Name)
		case cmd.Description == "":
			t.Errorf("%s: no description", cmd.Name)
		case !groups[cmd.Group]:
			t.Errorf("%s: group %q is not in groupOrder, so it would never be listed", cmd.Name, cmd.Group)
		case cmd.Run == nil && len(cmd.Subcommands) == 0:
			t.Errorf("%s: no handler and no actions, so nothing could ever run it", cmd.Name)
		}
		// An action without a handler would be advertised in the catalogue and
		// then refuse to run, which is worse than not being advertised at all.
		for _, sub := range cmd.Subcommands {
			switch {
			case sub.Name == "" || sub.Usage == "" || sub.Desc == "":
				t.Errorf("%s: an action is not fully declared: %+v", cmd.Name, sub)
			case sub.Run == nil:
				t.Errorf("%s %s: no handler", cmd.Name, sub.Name)
			}
		}
		if byName[cmd.Name] != cmd {
			t.Errorf("%s: not reachable by name", cmd.Name)
		}
		for _, alias := range cmd.Aliases {
			if byName[alias] != cmd {
				t.Errorf("%s: alias %q is not reachable", cmd.Name, alias)
			}
		}
	}

	admin := &db.User{Role: "admin"}
	plain := &db.User{Role: "user"}
	if len(visibleCommands(plain)) >= len(visibleCommands(admin)) {
		t.Error("an administrator sees no more commands than an ordinary user")
	}
	for _, cmd := range visibleCommands(plain) {
		if cmd.AdminOnly {
			t.Errorf("%s is an administrator command but is offered to everyone", cmd.Name)
		}
	}
}

// help --json is the entry point for a program that has never seen this
// gateway: it has to describe everything that caller may do, and nothing it
// may not.
func TestHelpJSONListsEveryCommandTheCallerMayRun(t *testing.T) {
	s, database := newTestServer(t, nil)
	plain := newTestUser(t, database, "u1", "u@example.test", "user")
	admin := newTestUser(t, database, "u2", "a@example.test", "admin")

	for _, user := range []*db.User{plain, admin} {
		output, err := run(t, s, user, "help --json")
		if err != nil {
			t.Fatalf("help --json for %s: %v", user.Role, err)
		}
		if strings.Contains(output, "\r") {
			t.Error("the JSON catalogue carries carriage returns, which a parser on the other end would have to strip")
		}

		var doc helpDoc
		if err := json.Unmarshal([]byte(output), &doc); err != nil {
			t.Fatalf("help --json is not valid JSON: %v\n%s", err, output)
		}
		if doc.User.Email != user.Email || doc.User.Admin != (user.Role == "admin") {
			t.Errorf("the catalogue misreports the caller: %+v", doc.User)
		}
		if doc.Invocation.OneShot == "" || doc.Invocation.DirectVM == "" || doc.Invocation.Interactive == "" {
			t.Errorf("the catalogue does not say how to invoke the gateway: %+v", doc.Invocation)
		}

		listed := map[string]bool{}
		for _, cmd := range doc.Commands {
			listed[cmd.Name] = true
			if cmd.Usage == "" || cmd.Summary == "" {
				t.Errorf("%s is listed without a usage or summary", cmd.Name)
			}
		}
		for _, cmd := range registry {
			switch {
			case cmd.AdminOnly && user.Role != "admin" && listed[cmd.Name]:
				t.Errorf("%s is offered to a non-administrator", cmd.Name)
			case (!cmd.AdminOnly || user.Role == "admin") && !listed[cmd.Name]:
				t.Errorf("%s is missing from the catalogue", cmd.Name)
			}
		}
	}
}

// The shell advertises "help <command> <action>" in its own output, so it has
// to answer it — and an action that does not exist has to say which do.
func TestHelpExplainsOneAction(t *testing.T) {
	s, database := newTestServer(t, nil)
	user := newTestUser(t, database, "u1", "u@example.test", "user")

	output, err := run(t, s, user, "help share revoke")
	if err != nil {
		t.Fatalf("help share revoke: %v", err)
	}
	if !strings.Contains(output, "share revoke <token>") {
		t.Errorf("the action's own usage is missing:\n%s", output)
	}
	// The group's other flags belong to other actions and must not appear here.
	if strings.Contains(output, "--ttl") {
		t.Errorf("an action was described with a flag it does not take:\n%s", output)
	}

	if _, err := run(t, s, user, "help share frobnicate"); err == nil {
		t.Error("help explained an action that does not exist")
	}
}

// A command hidden from an ordinary user must not describe itself through the
// error path either: "admin" alone used to answer with the list of actions.
func TestAdminSurfaceDoesNotLeakThroughErrors(t *testing.T) {
	s, database := newTestServer(t, nil)
	user := newTestUser(t, database, "u1", "u@example.test", "user")

	for _, line := range []string{"admin", "admin bogus", "admin users --json"} {
		_, err := run(t, s, user, line)
		if err == nil {
			t.Fatalf("%q was accepted from a non-administrator", line)
		}
		for _, leaked := range []string{"rmuser", "release", "nesting"} {
			if strings.Contains(err.Error(), leaked) {
				t.Errorf("%q named the administrator action %q: %v", line, leaked, err)
			}
		}
	}
}

// A flag given twice is a mistake of the same kind as a flag given a malformed
// value: quietly keeping the last one carries out an instruction nobody gave.
func TestRepeatedFlagIsRefused(t *testing.T) {
	s, database := newTestServer(t, nil)
	user := newTestUser(t, database, "u1", "u@example.test", "user")

	_, err := run(t, s, user, "new dev --cpu=4 --cpu=8")
	if err == nil {
		t.Fatal("a flag given twice was accepted")
	}
	if !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("the refusal does not explain itself: %v", err)
	}
}

// A misspelt action is the usual reason a grouped command gets an argument too
// many, so the message has to name the word that was wrong.
func TestMisspeltActionIsNamed(t *testing.T) {
	s, database := newTestServer(t, nil)
	user := newTestUser(t, database, "u1", "u@example.test", "user")

	_, err := run(t, s, user, "share bogus dev")
	if err == nil {
		t.Fatal("a misspelt action was accepted")
	}
	if !strings.Contains(err.Error(), `no action "bogus"`) {
		t.Fatalf("the message names the wrong token: %v", err)
	}
}

// A client that never sends a newline must be cut off at the advertised bound
// rather than growing the gateway's memory for as long as it keeps sending.
func TestLineLengthIsBounded(t *testing.T) {
	le := &lineEditor{}
	_, err := le.readPlain(strings.NewReader(strings.Repeat("x", maxLineLen*3)))
	if !errors.Is(err, errLineTooLong) {
		t.Fatalf("an endless line was accepted: %v", err)
	}
	if len(le.pending) > maxLineLen+1024 {
		t.Fatalf("the buffer grew to %d bytes past the %d-byte bound", len(le.pending), maxLineLen)
	}

	// Two commands arriving in one read are still two commands: the second is
	// returned from what the first read buffered, not from the reader.
	le = &lineEditor{}
	both := strings.NewReader("ls\nwhoami\n")
	for _, want := range []string{"ls", "whoami"} {
		got, err := le.readPlain(both)
		if err != nil || got != want {
			t.Fatalf("read %q (%v), want %q", got, err, want)
		}
	}
}

func TestHelpExplainsOneCommand(t *testing.T) {
	s, database := newTestServer(t, nil)
	user := newTestUser(t, database, "u1", "u@example.test", "user")

	output, err := run(t, s, user, "help new")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"new <name>", "Arguments:", "Examples:"} {
		if !strings.Contains(output, want) {
			t.Errorf("help new does not mention %q:\n%s", want, output)
		}
	}

	if _, err := run(t, s, user, "help definitely-not-a-command"); err == nil {
		t.Error("help explained a command that does not exist")
	}
}

// --json is what an agent reads, so it has to survive being piped into a
// parser: one document, built from the typed views, with nothing a terminal
// would have needed.
func TestReadCommandsEmitParsableJSON(t *testing.T) {
	s, database := newTestServer(t, nil)
	user := newTestUser(t, database, "u1", "u@example.test", "user")
	if err := database.CreateContainer(&db.Container{
		ID: "vm", Name: "dev", IncusName: "svkexe-u1-dev", OwnerID: user.ID,
		Status: "running", CPULimit: 2, MemoryMB: 2048, DiskGB: 10, AppPort: 3000,
	}); err != nil {
		t.Fatal(err)
	}

	output, err := run(t, s, user, "ls --json")
	if err != nil {
		t.Fatal(err)
	}
	var list []vmJSON
	if err := json.Unmarshal([]byte(output), &list); err != nil {
		t.Fatalf("ls --json is not valid JSON: %v\n%s", err, output)
	}
	if len(list) != 1 || list[0].Name != "dev" || list[0].URL != "https://dev.example.test" {
		t.Fatalf("ls --json does not describe the VM: %+v", list)
	}

	output, err = run(t, s, user, "stat dev --json")
	if err != nil {
		t.Fatal(err)
	}
	var one vmJSON
	if err := json.Unmarshal([]byte(output), &one); err != nil {
		t.Fatalf("stat --json is not valid JSON: %v\n%s", err, output)
	}
	if one.AppPort != 3000 || one.MemoryMB != 2048 || one.Status != "running" {
		t.Fatalf("stat --json misreports the VM: %+v", one)
	}

	for _, line := range []string{"ls --json", "stat dev --json", "whoami --json"} {
		output, err := run(t, s, user, line)
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsAny(output, "\r\x1b") {
			t.Errorf("%q emitted a carriage return or an escape sequence: %q", line, output)
		}
		if strings.Count(strings.TrimSpace(output), "\n") != 0 {
			t.Errorf("%q emitted more than one line: %q", line, output)
		}
	}
}

// A flag belongs to one action, not to the word it is grouped under. An action
// that writes something has no JSON to emit, and one that lists share links has
// no expiry to set: accepting either and then ignoring it would tell an agent
// its instruction was carried out.
func TestFlagsBelongToTheActionNotTheGroup(t *testing.T) {
	s, database := newTestServer(t, nil)
	user := newTestUser(t, database, "u1", "u@example.test", "user")
	admin := newTestUser(t, database, "a1", "a@example.test", "admin")

	refused := []struct {
		user *db.User
		line string
	}{
		{user, "task retry dev --json"},
		{user, "agent update dev --json"},
		{user, "share revoke token --json"},
		{user, "ssh-key add laptop ssh-ed25519 AAAA --json"},
		{user, "llm default auto --json"},
		{user, "domain add dev host.example.org --json"},
		{user, "share list dev --ttl=24h"},
		{user, "share revoke token --ttl=24h"},
		{admin, "admin rmuser someone --json"},
		{admin, "admin users --force"},
	}
	for _, tc := range refused {
		t.Run(tc.line, func(t *testing.T) {
			_, err := run(t, s, tc.user, tc.line)
			if err == nil {
				t.Fatalf("%q accepted a flag that action does not have", tc.line)
			}
			if !strings.Contains(err.Error(), "unknown flag") {
				t.Fatalf("%q failed for the wrong reason: %v", tc.line, err)
			}
		})
	}

	// The reading actions of the same commands still take --json.
	for _, line := range []string{"llm models --json", "ssh-key list --json", "share list dev --json"} {
		if _, err := run(t, s, user, line); err != nil && strings.Contains(err.Error(), "unknown flag") {
			t.Errorf("%q no longer accepts --json: %v", line, err)
		}
	}
}

// An unknown flag is a typo, and the caller — human or agent — can only correct
// it if it is named rather than ignored.
func TestUnknownFlagIsRefused(t *testing.T) {
	s, database := newTestServer(t, nil)
	user := newTestUser(t, database, "u1", "u@example.test", "user")

	if _, err := run(t, s, user, "ls --jsn"); err == nil {
		t.Fatal("a misspelled flag was accepted")
	}
}

func TestSplitArgsKeepsQuotedArguments(t *testing.T) {
	cases := []struct {
		line string
		want []string
	}{
		{line: `new dev --task="write a parser"`, want: []string{"new", "dev", "--task=write a parser"}},
		{line: `ssh-key add laptop ssh-ed25519 AAAA body`, want: []string{"ssh-key", "add", "laptop", "ssh-ed25519", "AAAA", "body"}},
		{line: `  ls   `, want: []string{"ls"}},
	}
	for _, tc := range cases {
		got, err := splitArgs(tc.line)
		if err != nil {
			t.Fatalf("%q: %v", tc.line, err)
		}
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("%q split into %q, want %q", tc.line, got, tc.want)
		}
	}
	if _, err := splitArgs(`new "unbalanced`); err == nil {
		t.Error("an unbalanced quote was accepted")
	}
}

// A terminal in raw mode needs a carriage return on every line; a parser must
// not be handed one. The same command therefore writes differently to each.
func TestOutTranslatesNewlinesOnlyForTerminals(t *testing.T) {
	var terminal, pipe bytes.Buffer
	newOut(&terminal, true).print("a\nb\n")
	newOut(&pipe, false).print("a\nb\n")

	if terminal.String() != "a\r\nb\r\n" {
		t.Errorf("terminal output %q", terminal.String())
	}
	if pipe.String() != "a\nb\n" {
		t.Errorf("piped output %q", pipe.String())
	}
}
