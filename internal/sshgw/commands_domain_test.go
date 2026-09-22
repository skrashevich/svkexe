package sshgw

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/skrashevich/svkexe/internal/aliases"
	"github.com/skrashevich/svkexe/internal/db"
)

// stubVerifier stands in for the DNS check. Its answer is a field rather than a
// constructor argument because the interesting sequence is an owner fixing
// their record: the same alias fails, then passes.
type stubVerifier struct{ err error }

func (v *stubVerifier) Verify(context.Context, string) error { return v.err }

// domainFixture builds a gateway with one VM owned by the returned user and a
// second VM owned by somebody else, which is what the ownership checks are
// tested against. A nil verifier is a deployment that cannot check DNS at all.
func domainFixture(t *testing.T, verifier aliases.Verifier) (*Server, *db.User) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	user := &db.User{ID: "u", Email: "u@example.test", Role: "user"}
	stranger := &db.User{ID: "o", Email: "o@example.test", Role: "user"}
	for _, u := range []*db.User{user, stranger} {
		if err := database.CreateUser(u); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.CreateContainer(&db.Container{ID: "vm", Name: "dev", IncusName: "incus-dev", OwnerID: user.ID, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateContainer(&db.Container{ID: "vm2", Name: "theirs", IncusName: "incus-theirs", OwnerID: stranger.ID, Status: "running"}); err != nil {
		t.Fatal(err)
	}

	s := &Server{db: database, domain: "svk.example"}
	// The runtime is nil on purpose: the manager only uses it to refresh the
	// agent's guide, which it skips when there is none.
	s.aliases = aliases.New(database, nil, verifier, s.domain)
	return s, user
}

func TestSSHDomainAddListRemove(t *testing.T) {
	// The reference an owner has to hand differs by where they read it: the
	// hostname from the text listing, the id from the JSON one. Both resolve.
	for _, byID := range []bool{false, true} {
		t.Run(map[bool]string{false: "by_hostname", true: "by_alias_id"}[byID], func(t *testing.T) {
			s, user := domainFixture(t, &stubVerifier{})

			out, err := run(t, s, user, "domain add dev app.example.org")
			if err != nil {
				t.Fatalf("add: %v", err)
			}
			if !strings.Contains(out, "app.example.org") || !strings.Contains(out, "routed") {
				t.Fatalf("add did not report the domain as routed: %q", out)
			}

			out, err = run(t, s, user, "domain list dev")
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if !strings.Contains(out, "app.example.org") || !strings.Contains(out, "verified") {
				t.Fatalf("list did not show the verified domain: %q", out)
			}

			raw, err := run(t, s, user, "domain list dev --json")
			if err != nil {
				t.Fatalf("list --json: %v", err)
			}
			var views []domainJSON
			if err := json.Unmarshal([]byte(raw), &views); err != nil {
				t.Fatalf("list --json emitted %q: %v", raw, err)
			}
			if len(views) != 1 {
				t.Fatalf("expected one domain, got %d: %q", len(views), raw)
			}
			if views[0].Hostname != "app.example.org" || !views[0].Verified || views[0].ID == "" || views[0].VerifiedAt == "" {
				t.Fatalf("JSON view is missing what verify and rm need: %+v", views[0])
			}

			ref := "app.example.org"
			if byID {
				ref = views[0].ID
			}
			out, err = run(t, s, user, "domain rm dev "+ref)
			if err != nil {
				t.Fatalf("rm: %v", err)
			}
			if !strings.Contains(out, "removed") {
				t.Fatalf("rm did not report the removal: %q", out)
			}
			left, err := s.db.ListAliasesByContainer("vm")
			if err != nil {
				t.Fatal(err)
			}
			if len(left) != 0 {
				t.Fatalf("rm left %d claims behind", len(left))
			}
		})
	}
}

// A hostname whose DNS does not point here yet is not a failed command: the row
// is kept with the reason, because what has to change is a record in the
// owner's zone rather than the name they typed.
func TestSSHDomainAddKeepsAnUnverifiedClaim(t *testing.T) {
	s, user := domainFixture(t, &stubVerifier{err: errors.New("app.example.org points at 203.0.113.9")})

	out, err := run(t, s, user, "domain add dev app.example.org")
	if err != nil {
		t.Fatalf("a failed DNS check failed the command: %v", err)
	}
	if !strings.Contains(out, "points at 203.0.113.9") {
		t.Errorf("add did not print why the check failed: %q", out)
	}
	if !strings.Contains(out, "svk.example") || !strings.Contains(out, "domain verify dev app.example.org") {
		t.Errorf("add did not say how the name becomes routable: %q", out)
	}

	stored, err := s.db.ListAliasesByContainer("vm")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Fatalf("the failed check did not keep the claim: %d rows", len(stored))
	}
	if stored[0].Verified {
		t.Error("an unchecked hostname was routed")
	}
	if stored[0].LastError == "" {
		t.Error("the reason was not recorded for the owner to read")
	}

	list, err := run(t, s, user, "domain list dev")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list, "unverified: app.example.org points at 203.0.113.9") {
		t.Errorf("list did not carry the stored reason: %q", list)
	}
}

// The point of keeping a failed claim is that the owner can fix their DNS and
// re-check it, naming the hostname they already know.
func TestSSHDomainVerifyByHostnameAfterTheRecordIsFixed(t *testing.T) {
	verifier := &stubVerifier{err: errors.New("no record found")}
	s, user := domainFixture(t, verifier)

	if _, err := run(t, s, user, "domain add dev app.example.org"); err != nil {
		t.Fatal(err)
	}
	verifier.err = nil

	out, err := run(t, s, user, "domain verify dev app.example.org")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !strings.Contains(out, "verified") || !strings.Contains(out, "routed") {
		t.Errorf("verify did not report the domain as routed: %q", out)
	}

	stored, err := s.db.ListAliasesByContainer("vm")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || !stored[0].Verified {
		t.Fatalf("the re-check did not start routing the domain: %+v", stored)
	}
}

// A deployment with no DNS verifier cannot make any name routable. Saying so is
// the whole point of aliases.ErrNoVerifier: the alternative is an owner chasing
// a DNS record that was never going to be read.
func TestSSHDomainWithoutAVerifierSaysDNSCannotBeChecked(t *testing.T) {
	s, user := domainFixture(t, nil)

	out, err := run(t, s, user, "domain add dev app.example.org")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(out, aliases.ErrNoVerifier.Error()) {
		t.Errorf("add hid that verification is not configured: %q", out)
	}
	if !strings.Contains(out, "cannot check DNS") {
		t.Errorf("add did not explain that no domain can become routable: %q", out)
	}
	if strings.Contains(out, "CNAME") {
		t.Errorf("add sent the owner after a DNS record nothing will read: %q", out)
	}

	stored, err := s.db.ListAliasesByContainer("vm")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].Verified {
		t.Fatalf("an unverifiable claim was lost or routed: %+v", stored)
	}
}

// Without a base domain the gateway has no alias manager at all. Every
// subcommand has to answer for that rather than dereference it.
func TestSSHDomainWithoutAManagerRefusesEverySubcommand(t *testing.T) {
	s, user := domainFixture(t, &stubVerifier{})
	s.aliases = nil

	for _, line := range []string{
		"domain list dev",
		"domain add dev app.example.org",
		"domain verify dev app.example.org",
		"domain rm dev app.example.org",
	} {
		t.Run(line, func(t *testing.T) {
			out, err := run(t, s, user, line)
			if err == nil {
				t.Fatalf("the command succeeded without an alias manager: %q", out)
			}
			if !errors.Is(err, errNoAliasManager) {
				t.Fatalf("unexpected failure: %v", err)
			}
			if out != "" {
				t.Errorf("a refused command still printed %q", out)
			}
		})
	}
}

// Every subcommand is scoped to the caller's own VMs, and an alias id from
// another VM does not become reachable by naming one's own.
func TestSSHDomainDoesNotReachAnotherOwnersVM(t *testing.T) {
	s, user := domainFixture(t, &stubVerifier{})

	stranger, err := s.db.GetContainerByID("vm2")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := s.aliases.Add(t.Context(), stranger, "theirs.example.org")
	if err != nil {
		t.Fatal(err)
	}

	for _, line := range []string{
		"domain list theirs",
		"domain add theirs app.example.org",
		"domain verify theirs theirs.example.org",
		"domain rm theirs theirs.example.org",
	} {
		if _, err := run(t, s, user, line); err == nil || !strings.Contains(err.Error(), `VM "theirs" not found`) {
			t.Errorf("%q reached another owner's VM: %v", line, err)
		}
	}

	for _, ref := range []string{theirs.ID, theirs.Hostname} {
		if _, err := run(t, s, user, "domain rm dev "+ref); err == nil || !strings.Contains(err.Error(), "no custom domain") {
			t.Errorf("another VM's claim %q was reachable through dev: %v", ref, err)
		}
	}
	left, err := s.db.ListAliasesByContainer("vm2")
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 {
		t.Fatalf("another owner's claim was removed: %d rows", len(left))
	}
}

// A hostname this gateway will never accept is reported with the reason it was
// rejected, which is the only thing that tells the owner what to type instead.
func TestSSHDomainAddReportsWhyAHostnameIsInvalid(t *testing.T) {
	s, user := domainFixture(t, &stubVerifier{})

	for _, tc := range []struct {
		hostname string
		reason   string
	}{
		{"nodots", "full domain name"},
		{"app.svk.example", "already routed by this gateway"},
	} {
		t.Run(tc.hostname, func(t *testing.T) {
			_, err := run(t, s, user, "domain add dev "+tc.hostname)
			if err == nil {
				t.Fatal("an unusable hostname was accepted")
			}
			if !errors.Is(err, aliases.ErrInvalid) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("the reason did not reach the owner: %v", err)
			}
		})
	}
}

// A malformed invocation is answered with the shape of what was invoked,
// because an agent can correct itself from that without another round trip.
// "domain" has no bare form, so the word alone — and any word that is not one
// of its actions — is answered with the list of actions.
func TestSSHDomainUsageErrors(t *testing.T) {
	s, user := domainFixture(t, &stubVerifier{})

	for _, tc := range []struct{ line, want string }{
		{"domain", "list, add, verify, rm"},
		{"domain frobnicate dev", "list, add, verify, rm"},
		{"domain list", "name a VM"},
		{"domain add dev", "name the domain to add"},
		{"domain verify dev", "name the domain"},
		// --json belongs to the reading action alone: the parser validates a
		// flag against the action that was resolved, not against the union of
		// everything under the same word.
		{"domain add dev app.example.org --json", `unknown flag --json for "domain add"`},
	} {
		t.Run(tc.line, func(t *testing.T) {
			_, err := run(t, s, user, tc.line)
			if _, ok := errors.AsType[*usageError](err); !ok {
				t.Fatalf("expected a usage error, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("usage error does not say what to fix: %v", err)
			}
		})
	}
}
