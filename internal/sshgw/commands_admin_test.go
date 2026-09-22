package sshgw

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skrashevich/svkexe/internal/aliases"
	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/updater"
	"github.com/skrashevich/svkexe/internal/version"
)

// adminTestServer is a gateway with nothing but a database: every command under
// test here reads and writes rows, and the two that need more — update and
// release — are exercised precisely for what they do when that dependency is
// absent.
func adminTestServer(t *testing.T) *Server {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return &Server{db: database}
}

func adminTestUser(t *testing.T, s *Server, id, email, role string) *db.User {
	t.Helper()
	user := &db.User{ID: id, Email: email, Role: role}
	if err := s.db.CreateUser(user); err != nil {
		t.Fatal(err)
	}
	return user
}

func adminTestVM(t *testing.T, s *Server, id, name string, owner *db.User) *db.Container {
	t.Helper()
	c := &db.Container{
		ID: id, Name: name, IncusName: "svkexe-" + owner.ID + "-" + name,
		OwnerID: owner.ID, Status: "running", CPULimit: 2, MemoryMB: 2048, DiskGB: 10,
	}
	if err := s.db.CreateContainer(c); err != nil {
		t.Fatal(err)
	}
	return c
}

// The administration group is the one place where the shell decides what a user
// may see rather than only what they may reach, so both halves are checked: the
// refusal, and the absence of the command from anything that lists commands.
func TestAdminCommandsAreRefusedAndHiddenFromOrdinaryUsers(t *testing.T) {
	s := adminTestServer(t)
	admin := adminTestUser(t, s, "a", "admin@example.test", "admin")
	user := adminTestUser(t, s, "u", "user@example.test", "user")

	output, err := run(t, s, user, "admin users")
	if err == nil {
		t.Fatal("an ordinary user ran an administrator command")
	}
	if !strings.Contains(err.Error(), "administrator") {
		t.Errorf("refusal does not say why: %v", err)
	}
	if output != "" {
		t.Errorf("a refused command still wrote output: %q", output)
	}
	if _, err := run(t, s, user, "help admin"); err == nil {
		t.Error("help described an administrator command to an ordinary user")
	}

	for _, cmd := range visibleCommands(user) {
		if cmd.Group == groupAdmin {
			t.Errorf("command %q is offered to an ordinary user", cmd.Name)
		}
	}
	var adminSees bool
	for _, cmd := range visibleCommands(admin) {
		if cmd.Group == groupAdmin {
			adminSees = true
		}
	}
	if !adminSees {
		t.Error("an administrator is offered no administration commands")
	}
}

func TestAdminListsAccountsAndVMsAcrossOwners(t *testing.T) {
	s := adminTestServer(t)
	admin := adminTestUser(t, s, "a", "admin@example.test", "admin")
	alice := adminTestUser(t, s, "alice", "alice@example.test", "user")
	adminTestVM(t, s, "vm-ops", "ops", admin)
	adminTestVM(t, s, "vm-dev", "dev", alice)
	adminTestVM(t, s, "vm-ci", "ci", alice)

	cases := []struct {
		name string
		line string
		want []string
	}{
		{"users", "admin users", []string{"admin@example.test", "alice@example.test", "admin", "user"}},
		{"vms", "admin vms", []string{"ops", "dev", "ci", "alice@example.test", "admin@example.test"}},
		{"unknown action", "admin frobnicate", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			output, err := run(t, s, admin, tc.line)
			if tc.want == nil {
				if err == nil {
					t.Fatalf("%q was accepted", tc.line)
				}
				return
			}
			if err != nil {
				t.Fatalf("%q: %v", tc.line, err)
			}
			for _, want := range tc.want {
				if !strings.Contains(output, want) {
					t.Errorf("%q does not mention %q:\n%s", tc.line, want, output)
				}
			}
		})
	}

	t.Run("users json", func(t *testing.T) {
		output, err := run(t, s, admin, "admin users --json")
		if err != nil {
			t.Fatal(err)
		}
		var users []adminUserJSON
		if err := json.Unmarshal([]byte(output), &users); err != nil {
			t.Fatalf("parse %q: %v", output, err)
		}
		if len(users) != 2 {
			t.Fatalf("want 2 accounts, got %d", len(users))
		}
		counts := map[string]int{}
		for _, u := range users {
			counts[u.Email] = u.VMs
		}
		if counts["alice@example.test"] != 2 || counts["admin@example.test"] != 1 {
			t.Errorf("wrong VM counts: %v", counts)
		}
	})

	t.Run("vms json", func(t *testing.T) {
		output, err := run(t, s, admin, "admin vms --json")
		if err != nil {
			t.Fatal(err)
		}
		var vms []adminVMJSON
		if err := json.Unmarshal([]byte(output), &vms); err != nil {
			t.Fatalf("parse %q: %v", output, err)
		}
		if len(vms) != 3 {
			t.Fatalf("want 3 VMs, got %d", len(vms))
		}
		for _, vm := range vms {
			if vm.OwnerEmail == "" || vm.Name == "" {
				t.Errorf("VM listed without a name or an owner: %+v", vm)
			}
		}
	})
}

func TestAdminDomainsListsEveryClaimWithItsHolder(t *testing.T) {
	s := adminTestServer(t)
	admin := adminTestUser(t, s, "a", "admin@example.test", "admin")
	alice := adminTestUser(t, s, "alice", "alice@example.test", "user")
	adminTestVM(t, s, "vm-dev", "dev", alice)

	alias, err := s.db.CreateContainerAlias("vm-dev", "app.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.SetAliasVerification(alias.ID, true, ""); err != nil {
		t.Fatal(err)
	}

	output, err := run(t, s, admin, "admin domains")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"app.example.test", "dev", "alice@example.test", "verified"} {
		if !strings.Contains(output, want) {
			t.Errorf("the domain table does not mention %q:\n%s", want, output)
		}
	}

	output, err = run(t, s, admin, "admin domains --json")
	if err != nil {
		t.Fatal(err)
	}
	var claims []adminAliasJSON
	if err := json.Unmarshal([]byte(output), &claims); err != nil {
		t.Fatalf("parse %q: %v", output, err)
	}
	if len(claims) != 1 || claims[0].OwnerEmail != alice.Email || !claims[0].Verified {
		t.Errorf("wrong claim list: %+v", claims)
	}
}

// The ceiling is what VMs may boot with, and an operator flipping it from the
// shell has to end up with the same stored setting the dashboard writes.
func TestAdminNestingSetsTheDeploymentCeiling(t *testing.T) {
	s := adminTestServer(t)
	admin := adminTestUser(t, s, "a", "admin@example.test", "admin")

	steps := []struct {
		line    string
		allowed bool
		says    string
	}{
		{"admin nesting off", false, "forbidden"},
		{"admin nesting", false, "forbidden"},
		{"admin nesting on", true, "allowed"},
		{"admin nesting", true, "allowed"},
	}
	for _, step := range steps {
		output, err := run(t, s, admin, step.line)
		if err != nil {
			t.Fatalf("%q: %v", step.line, err)
		}
		allowed, err := s.db.NestingAllowed()
		if err != nil {
			t.Fatal(err)
		}
		if allowed != step.allowed {
			t.Errorf("after %q the stored ceiling is %v, want %v", step.line, allowed, step.allowed)
		}
		if !strings.Contains(output, step.says) {
			t.Errorf("%q does not say %q:\n%s", step.line, step.says, output)
		}
		// A running VM keeps what it booted with, which is the half an operator
		// would otherwise misread as the change having failed.
		if !strings.Contains(output, "restarts") {
			t.Errorf("%q does not mention that running VMs keep their setting:\n%s", step.line, output)
		}
	}

	if _, err := run(t, s, admin, "admin nesting maybe"); err == nil {
		t.Error("an unrecognised nesting argument was accepted")
	}
}

func TestAdminRemoveUser(t *testing.T) {
	s := adminTestServer(t)
	admin := adminTestUser(t, s, "a", "admin@example.test", "admin")
	alice := adminTestUser(t, s, "alice", "alice@example.test", "user")
	bob := adminTestUser(t, s, "bob", "bob@example.test", "user")
	adminTestVM(t, s, "vm-dev", "dev", alice)

	t.Run("refuses the caller's own account", func(t *testing.T) {
		if _, err := run(t, s, admin, "admin rmuser "+admin.Email); err == nil {
			t.Fatal("an administrator deleted their own account")
		}
		if _, err := run(t, s, admin, "admin rmuser "+admin.ID); err == nil {
			t.Fatal("the refusal is bypassed by naming the account by id")
		}
		if _, err := s.db.GetUserByID(admin.ID); err != nil {
			t.Fatalf("the refused delete still removed the account: %v", err)
		}
	})

	t.Run("unknown account", func(t *testing.T) {
		if _, err := run(t, s, admin, "admin rmuser nobody@example.test"); err == nil {
			t.Fatal("a delete of an account that does not exist was accepted")
		}
	})

	t.Run("needs --force in a one-shot", func(t *testing.T) {
		if _, err := run(t, s, admin, "admin rmuser "+alice.Email); err == nil {
			t.Fatal("an account was deleted by a one-shot invocation without --force")
		}
		if _, err := s.db.GetUserByID(alice.ID); err != nil {
			t.Fatal("the account was deleted anyway")
		}
	})

	t.Run("by email, cascading", func(t *testing.T) {
		output, err := run(t, s, admin, "admin rmuser "+alice.Email+" --force")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.GetUserByID(alice.ID); err == nil {
			t.Error("the account survived its own deletion")
		}
		containers, err := s.db.ListAllContainers()
		if err != nil {
			t.Fatal(err)
		}
		if len(containers) != 0 {
			t.Errorf("the VM records survived the cascade: %d left", len(containers))
		}
		// The instances outliving the records is the thing an operator has to
		// know before running this, so the output has to say it.
		if !strings.Contains(output, "Incus") {
			t.Errorf("the deletion does not say what happens to the instances:\n%s", output)
		}
	})

	t.Run("by id", func(t *testing.T) {
		// Typed at the prompt, the operator has already said what they meant.
		if _, err := runInteractive(t, s, admin, "admin rmuser "+bob.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.GetUserByID(bob.ID); err == nil {
			t.Error("the account survived its own deletion")
		}
	})
}

// A deployment can be built without the update wiring and without a domain
// manager. Both are supported, so the commands that need them have to say so
// rather than panic on a nil dependency.
func TestAdminRefusesWhenItsDependencyIsMissing(t *testing.T) {
	s := adminTestServer(t)
	admin := adminTestUser(t, s, "a", "admin@example.test", "admin")

	cases := []struct {
		line string
		says string
	}{
		{"admin update", "self-update is not configured"},
		{"admin update status", "self-update is not configured"},
		{"admin update check", "self-update is not configured"},
		{"admin update start", "self-update is not configured"},
		{"admin release app.example.test", "custom domains are not configured"},
	}
	for _, tc := range cases {
		output, err := run(t, s, admin, tc.line)
		if err == nil {
			t.Fatalf("%q was accepted without its dependency", tc.line)
		}
		if !strings.Contains(err.Error(), tc.says) {
			t.Errorf("%q says %q, want it to mention %q", tc.line, err, tc.says)
		}
		if output != "" {
			t.Errorf("%q wrote output before refusing: %q", tc.line, output)
		}
	}

	if _, err := run(t, s, admin, "admin release"); err == nil {
		t.Error("a release with no hostname was accepted")
	}
}

// Releasing a hostname is the operator's answer to a name held by the wrong
// account, so the test that matters is the one where it actually lets go.
func TestAdminReleaseFreesAHostnameForAnotherAccount(t *testing.T) {
	s := adminTestServer(t)
	admin := adminTestUser(t, s, "a1", "admin@example.test", "admin")
	holder := adminTestUser(t, s, "u1", "holder@example.test", "user")
	claimant := adminTestUser(t, s, "u2", "claimant@example.test", "user")
	adminTestVM(t, s, "vm1", "held", holder)
	adminTestVM(t, s, "vm2", "wanted", claimant)
	s.domain = "svk.example"
	s.aliases = aliases.New(s.db, nil, &stubVerifier{}, s.domain)

	if _, err := run(t, s, holder, "domain add held shop.example.org"); err != nil {
		t.Fatalf("the holder could not claim the hostname: %v", err)
	}
	// While it is held, nobody else can have it.
	if _, err := run(t, s, claimant, "domain add wanted shop.example.org"); err == nil {
		t.Fatal("a hostname held by another account was claimed anyway")
	}

	output, err := run(t, s, admin, "admin release shop.example.org")
	if err != nil {
		t.Fatalf("admin release: %v", err)
	}
	if !strings.Contains(output, "1") {
		t.Errorf("the release does not say how many claims it removed:\n%s", output)
	}
	if claims, err := s.db.ListAliasesByHostname("shop.example.org"); err != nil || len(claims) != 0 {
		t.Fatalf("the claim survived the release: %v %v", claims, err)
	}
	if _, err := run(t, s, claimant, "domain add wanted shop.example.org"); err != nil {
		t.Fatalf("the freed hostname could not be claimed: %v", err)
	}

	// A name nobody holds is not silently reported as released.
	if _, err := run(t, s, admin, "admin release nobody.example.org"); err == nil {
		t.Error("releasing an unclaimed hostname reported success")
	}
}

// The update actions are what an operator reaches for when the gateway is
// behind, so each one has to answer from the real service rather than only
// refuse when it is absent.
func TestAdminUpdateReportsChecksAndStarts(t *testing.T) {
	s := adminTestServer(t)
	admin := adminTestUser(t, s, "a1", "admin@example.test", "admin")

	dir := t.TempDir()
	// Stand in for the marker the root-owned update units write; without it the
	// runner correctly refuses to write a trigger nothing would consume.
	if err := os.WriteFile(filepath.Join(dir, "update-watcher"), []byte("svkexe-update.path\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"sha":%q,"html_url":"https://example.invalid/c","commit":{"committer":{"date":"2026-01-02T03:04:05Z"}}}`, strings.Repeat("b", 40))
	}))
	t.Cleanup(github.Close)

	local := version.Info{Version: "v9.9.9", Commit: strings.Repeat("a", 40), BuildDate: "2026-01-01T00:00:00Z"}
	s.updater = updater.NewService(
		updater.Config{APIBase: github.URL, Local: &local},
		updater.RunnerConfig{
			TriggerPath: filepath.Join(dir, "update.trigger"),
			StatusPath:  filepath.Join(dir, "update-status.json"),
			WatcherPath: filepath.Join(dir, "update-watcher"),
		},
	)

	status, err := run(t, s, admin, "admin update")
	if err != nil {
		t.Fatalf("admin update: %v", err)
	}
	if !strings.Contains(status, "v9.9.9") {
		t.Errorf("the status does not name the running build:\n%s", status)
	}

	raw, err := run(t, s, admin, "admin update check --json")
	if err != nil {
		t.Fatalf("admin update check: %v", err)
	}
	var report adminUpdateJSON
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatalf("the check is not valid JSON: %v\n%s", err, raw)
	}
	if report.Check == nil || report.Check.Latest == nil {
		t.Fatalf("the check reports nothing upstream: %s", raw)
	}
	if !report.Check.UpdateAvailable {
		t.Errorf("a remote commit different from the local one was not reported as an update: %s", raw)
	}

	if _, err := run(t, s, admin, "admin update start"); err != nil {
		t.Fatalf("admin update start: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "update.trigger")); err != nil {
		t.Fatalf("no trigger was written for the update units: %v", err)
	}

	if _, err := run(t, s, admin, "admin update frobnicate"); err == nil {
		t.Error("an unknown update action was accepted")
	}
}
