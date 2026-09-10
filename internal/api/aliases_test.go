package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dbpkg "github.com/skrashevich/svkexe/internal/db"
)

// stubVerifier stands in for the DNS check so no test touches real DNS.
type stubVerifier struct {
	// accept lists the hostnames that are treated as pointing at this gateway.
	accept map[string]bool
	// asked records what was checked, so a test can show the check ran at all.
	asked []string
}

func (s *stubVerifier) Verify(_ context.Context, hostname string) error {
	s.asked = append(s.asked, hostname)
	if s.accept[hostname] {
		return nil
	}
	return errors.New("does not point at this gateway")
}

// newAliasTestServer builds a server with a configured domain (aliases are
// checked against it) and a stub verifier, plus a VM owned by user1 and a
// second user to test ownership with.
func newAliasTestServer(t *testing.T) (*Server, *dbpkg.DB, *stubVerifier) {
	t.Helper()
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	for _, u := range []*dbpkg.User{
		{ID: "user1", Email: "user1@example.com", Role: "user"},
		{ID: "user2", Email: "user2@example.com", Role: "user"},
	} {
		if err := database.CreateUser(u); err != nil {
			t.Fatalf("create user: %v", err)
		}
	}
	sess, err := database.CreateSession("user1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	testSessionToken = sess.Token

	for _, c := range []*dbpkg.Container{
		{ID: "vm1", Name: "box", OwnerID: "user1", IncusName: "svkexe-user1-box", Status: "running"},
		{ID: "vm2", Name: "second", OwnerID: "user1", IncusName: "svkexe-user1-second", Status: "running"},
		{ID: "vm3", Name: "theirs", OwnerID: "user2", IncusName: "svkexe-user2-theirs", Status: "running"},
	} {
		if err := database.CreateContainer(c); err != nil {
			t.Fatalf("create container: %v", err)
		}
	}

	verifier := &stubVerifier{accept: map[string]bool{}}
	// The runtime is nil: there is no VM to write an agent guide into, and a
	// missing runtime must not fail an alias operation.
	srv := NewServer(database, nil, testEncKey, "example.com", nil, nil, nil, nil, nil, verifier)
	return srv, database, verifier
}

func addAlias(t *testing.T, srv *Server, vmID, hostname string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(aliasRequest{Hostname: hostname})
	if err != nil {
		t.Fatal(err)
	}
	return do(srv, authedRequest(http.MethodPost, "/api/containers/"+vmID+"/aliases", body))
}

func decodeAlias(t *testing.T, w *httptest.ResponseRecorder) *dbpkg.ContainerAlias {
	t.Helper()
	a := &dbpkg.ContainerAlias{}
	if err := json.Unmarshal(w.Body.Bytes(), a); err != nil {
		t.Fatalf("decode alias from %q: %v", w.Body.String(), err)
	}
	return a
}

func TestCreateAliasVerifiesImmediately(t *testing.T) {
	srv, _, verifier := newAliasTestServer(t)
	verifier.accept["app.example.org"] = true

	w := addAlias(t, srv, "vm1", "  App.Example.ORG.  ")
	if w.Code != http.StatusCreated {
		t.Fatalf("create alias: %d %s", w.Code, w.Body.String())
	}
	a := decodeAlias(t, w)
	if a.Hostname != "app.example.org" {
		t.Errorf("hostname was not normalised: %q", a.Hostname)
	}
	if !a.Verified {
		t.Errorf("alias should be verified, last error %q", a.LastError)
	}
	if a.LastError != "" {
		t.Errorf("a verified alias must carry no error, got %q", a.LastError)
	}
	if len(verifier.asked) != 1 || verifier.asked[0] != "app.example.org" {
		t.Errorf("verifier was asked about %v", verifier.asked)
	}
}

// DNS takes time to propagate. Refusing to store the hostname would make the
// owner retype it later; storing it unverified lets them re-check instead.
func TestCreateAliasKeepsUnverifiedHostname(t *testing.T) {
	srv, database, _ := newAliasTestServer(t)

	w := addAlias(t, srv, "vm1", "app.example.org")
	if w.Code != http.StatusCreated {
		t.Fatalf("create alias: %d %s", w.Code, w.Body.String())
	}
	a := decodeAlias(t, w)
	if a.Verified {
		t.Error("alias must not verify when DNS does not point here")
	}
	if a.LastError == "" {
		t.Error("the response must explain why the alias did not verify")
	}

	stored, err := database.ListAliasesByContainer("vm1")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Fatalf("want the hostname kept, got %d rows", len(stored))
	}
	// Nothing is routed until it verifies.
	if _, err := database.GetVerifiedAliasByHostname("app.example.org"); err == nil {
		t.Error("an unverified alias must not be routable")
	}
}

func TestCreateAliasRejectsBadHostnames(t *testing.T) {
	srv, _, _ := newAliasTestServer(t)

	cases := []struct {
		name     string
		hostname string
	}{
		{name: "not a domain", hostname: "localhost"},
		{name: "empty", hostname: ""},
		{name: "illegal characters", hostname: "my_app.example.org"},
		// The gateway's own namespace is routed by subdomain; an alias there
		// would shadow a VM host.
		{name: "the gateway domain", hostname: "example.com"},
		{name: "under the gateway domain", hostname: "box.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := addAlias(t, srv, "vm1", tc.hostname)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("%q: %d %s, want 400", tc.hostname, w.Code, w.Body.String())
			}
			if strings.TrimSpace(w.Body.String()) == "" {
				t.Error("the rejection must say what is wrong with the hostname")
			}
		})
	}
}

func TestCreateAliasRejectsDuplicateOnTheSameVM(t *testing.T) {
	srv, _, _ := newAliasTestServer(t)

	if code := addAlias(t, srv, "vm1", "app.example.org").Code; code != http.StatusCreated {
		t.Fatalf("first claim: %d", code)
	}
	if code := addAlias(t, srv, "vm1", "app.example.org").Code; code != http.StatusConflict {
		t.Errorf("the same VM claiming twice: %d, want 409", code)
	}
}

// Only a hostname somebody has actually proved they control is exclusive.
// Reserving it on an unverified claim would let anyone park a domain they do
// not own and lock out its real owner — what the DNS check exists to prevent.
func TestPendingClaimDoesNotLockOutTheRealOwner(t *testing.T) {
	srv, _, verifier := newAliasTestServer(t)

	// vm1 asks for a domain whose DNS does not point here.
	if code := addAlias(t, srv, "vm1", "app.example.org").Code; code != http.StatusCreated {
		t.Fatalf("first claim: %d", code)
	}
	// vm2 must still be able to ask for the same name.
	w := addAlias(t, srv, "vm2", "app.example.org")
	if w.Code != http.StatusCreated {
		t.Fatalf("second pending claim: %d %s, want 201", w.Code, w.Body.String())
	}

	// vm2's DNS is the one that points here, so vm2 gets the name.
	verifier.accept["app.example.org"] = true
	second := decodeAlias(t, w)
	verified := do(srv, authedRequest(http.MethodPost, "/api/containers/vm2/aliases/"+second.ID+"/verify", nil))
	if got := decodeAlias(t, verified); !got.Verified {
		t.Fatalf("the real owner could not verify their domain: %q", got.LastError)
	}

	// Now it is taken: nobody else may claim it.
	if code := addAlias(t, srv, "vm1", "other.example.org").Code; code != http.StatusCreated {
		t.Fatalf("unrelated claim: %d", code)
	}
	if code := addAlias(t, srv, "vm3", "app.example.org").Code; code == http.StatusCreated {
		t.Error("a verified hostname was claimed by another VM")
	}
}

// A claim parked on somebody else's domain is destroyed the moment they prove
// the name points here, so it cannot sit and wait for their verification to
// lapse and then take the traffic.
func TestVerifyingClearsAParkedClaim(t *testing.T) {
	srv, database, verifier := newAliasTestServer(t)

	// Both claims go in while neither resolves here, which is the only window
	// in which a competing claim can exist at all.
	parked := decodeAlias(t, addAlias(t, srv, "vm2", "app.example.org"))
	mine := decodeAlias(t, addAlias(t, srv, "vm1", "app.example.org"))

	verifier.accept["app.example.org"] = true
	won := do(srv, authedRequest(http.MethodPost, "/api/containers/vm1/aliases/"+mine.ID+"/verify", nil))
	if got := decodeAlias(t, won); !got.Verified {
		t.Fatalf("the owner could not verify: %q", got.LastError)
	}

	// The parked claim is gone, so its VM has nothing left to verify.
	if code := do(srv, authedRequest(http.MethodPost, "/api/containers/vm2/aliases/"+parked.ID+"/verify", nil)).Code; code != http.StatusNotFound {
		t.Errorf("parked claim survived the owner's verification: %d, want 404", code)
	}
	list, err := database.ListAliasesByContainer("vm2")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("parked claim still on the other VM: %+v", list)
	}

	// And the name stays with the VM that proved it.
	owner, err := database.GetVerifiedAliasByHostname("app.example.org")
	if err != nil || owner.ContainerID != "vm1" {
		t.Errorf("the hostname changed hands: %+v, %v", owner, err)
	}

	// Even after the owner's verification lapses, nobody inherits the name.
	if err := database.SetAliasVerification(mine.ID, false, "transient failure"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetVerifiedAliasByHostname("app.example.org"); err == nil {
		t.Error("the hostname still routes after its verification lapsed")
	}
}

// The owner is told a domain is already theirs, not that somebody else has it.
func TestReAddingYourOwnDomainSaysSo(t *testing.T) {
	srv, _, _ := newAliasTestServer(t)

	if code := addAlias(t, srv, "vm1", "app.example.org").Code; code != http.StatusCreated {
		t.Fatalf("first claim: %d", code)
	}
	w := addAlias(t, srv, "vm1", "app.example.org")
	if w.Code != http.StatusConflict {
		t.Fatalf("re-adding: %d, want 409", w.Code)
	}
	if strings.Contains(w.Body.String(), "another VM") {
		t.Errorf("the owner is told their own domain belongs to somebody else: %q", w.Body.String())
	}
}

func TestListAliases(t *testing.T) {
	srv, _, _ := newAliasTestServer(t)

	w := do(srv, authedRequest(http.MethodGet, "/api/containers/vm1/aliases", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d", w.Code)
	}
	// An empty list must serialise as [], not null, so clients can iterate it.
	if got := strings.TrimSpace(w.Body.String()); got != "[]" {
		t.Errorf("empty list serialised as %q", got)
	}

	addAlias(t, srv, "vm1", "app.example.org")
	addAlias(t, srv, "vm2", "other.example.org")

	w = do(srv, authedRequest(http.MethodGet, "/api/containers/vm1/aliases", nil))
	var list []*dbpkg.ContainerAlias
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Hostname != "app.example.org" {
		t.Fatalf("a VM's list must hold only its own aliases: %+v", list)
	}
}

func TestVerifyAliasRechecks(t *testing.T) {
	srv, _, verifier := newAliasTestServer(t)

	a := decodeAlias(t, addAlias(t, srv, "vm1", "app.example.org"))
	if a.Verified {
		t.Fatal("alias verified before DNS pointed here")
	}

	// The owner fixes their record and asks again.
	verifier.accept["app.example.org"] = true
	w := do(srv, authedRequest(http.MethodPost, "/api/containers/vm1/aliases/"+a.ID+"/verify", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("verify: %d %s", w.Code, w.Body.String())
	}
	if got := decodeAlias(t, w); !got.Verified || got.LastError != "" {
		t.Fatalf("re-check did not clear the failure: %+v", got)
	}

	// A record that stops pointing here must stop being routed.
	delete(verifier.accept, "app.example.org")
	w = do(srv, authedRequest(http.MethodPost, "/api/containers/vm1/aliases/"+a.ID+"/verify", nil))
	if got := decodeAlias(t, w); got.Verified {
		t.Error("a failed re-check must clear the verified flag")
	}
}

func TestDeleteAliasFreesTheHostname(t *testing.T) {
	srv, database, _ := newAliasTestServer(t)
	a := decodeAlias(t, addAlias(t, srv, "vm1", "app.example.org"))

	w := do(srv, authedRequest(http.MethodDelete, "/api/containers/vm1/aliases/"+a.ID, nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	list, err := database.ListAliasesByContainer("vm1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("alias survived deletion: %+v", list)
	}
	if code := addAlias(t, srv, "vm2", "app.example.org").Code; code != http.StatusCreated {
		t.Errorf("a released hostname must be claimable again: %d", code)
	}
}

// An alias ID is only meaningful inside the VM whose route carries it;
// otherwise one VM's route becomes a way to reach another's aliases.
func TestAliasRoutesAreScopedToTheirContainer(t *testing.T) {
	srv, database, _ := newAliasTestServer(t)
	a := decodeAlias(t, addAlias(t, srv, "vm1", "app.example.org"))

	for _, req := range []*http.Request{
		authedRequest(http.MethodDelete, "/api/containers/vm2/aliases/"+a.ID, nil),
		authedRequest(http.MethodPost, "/api/containers/vm2/aliases/"+a.ID+"/verify", nil),
	} {
		if code := do(srv, req).Code; code != http.StatusNotFound {
			t.Errorf("%s %s: %d, want 404", req.Method, req.URL.Path, code)
		}
	}
	if list, _ := database.ListAliasesByContainer("vm1"); len(list) != 1 {
		t.Error("the alias was touched through another VM's route")
	}
}

func TestAliasRoutesEnforceOwnership(t *testing.T) {
	srv, database, verifier := newAliasTestServer(t)
	verifier.accept["theirs.example.org"] = true

	// Seed an alias on the other user's VM directly, since the API would not
	// let user1 create it.
	other, err := database.CreateContainerAlias("vm3", "theirs.example.org")
	if err != nil {
		t.Fatal(err)
	}

	requests := []*http.Request{
		authedRequest(http.MethodGet, "/api/containers/vm3/aliases", nil),
		authedRequest(http.MethodDelete, "/api/containers/vm3/aliases/"+other.ID, nil),
		authedRequest(http.MethodPost, "/api/containers/vm3/aliases/"+other.ID+"/verify", nil),
	}
	body, _ := json.Marshal(aliasRequest{Hostname: "grab.example.org"})
	requests = append(requests, authedRequest(http.MethodPost, "/api/containers/vm3/aliases", body))

	for _, req := range requests {
		if code := do(srv, req).Code; code != http.StatusForbidden {
			t.Errorf("%s %s: %d, want 403", req.Method, req.URL.Path, code)
		}
	}

	list, err := database.ListAliasesByContainer("vm3")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Hostname != "theirs.example.org" {
		t.Fatalf("another owner's aliases were modified: %+v", list)
	}
}

func TestAliasRoutesRequireASession(t *testing.T) {
	srv, _, _ := newAliasTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/api/containers/vm1/aliases", nil)
	if code := do(srv, r).Code; code != http.StatusUnauthorized {
		t.Errorf("anonymous alias list: %d, want 401", code)
	}
}

// --- Caddy's on-demand TLS ask ---

func TestTLSCheckAllowsOnlyVerifiedAliases(t *testing.T) {
	srv, _, verifier := newAliasTestServer(t)
	verifier.accept["app.example.org"] = true
	addAlias(t, srv, "vm1", "app.example.org")
	pending := decodeAlias(t, addAlias(t, srv, "vm2", "pending.example.org"))
	if pending.Verified {
		t.Fatal("the pending alias should not have verified")
	}

	cases := []struct {
		name  string
		query string
		want  int
	}{
		{name: "a verified alias", query: "?domain=app.example.org", want: http.StatusOK},
		{name: "case and trailing dot", query: "?domain=APP.Example.org.", want: http.StatusOK},
		{name: "an unverified alias", query: "?domain=pending.example.org", want: http.StatusNotFound},
		{name: "an unclaimed hostname", query: "?domain=nobody.example.org", want: http.StatusNotFound},
		// Certificates for the gateway's own names come from its configured
		// issuer, never from the on-demand path.
		{name: "the gateway domain", query: "?domain=example.com", want: http.StatusNotFound},
		{name: "a VM subdomain", query: "?domain=box.example.com", want: http.StatusNotFound},
		{name: "a malformed name", query: "?domain=not-a-domain", want: http.StatusNotFound},
		{name: "no domain at all", query: "", want: http.StatusBadRequest},
		{name: "an empty domain", query: "?domain=", want: http.StatusBadRequest},
		// Rejected on length before anything normalises it, so a caller cannot
		// decide how much this handler allocates.
		{name: "an absurdly long name", query: "?domain=" + strings.Repeat("a", 4000) + ".example.org", want: http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Caddy asks during a handshake and has no session to present.
			r := httptest.NewRequest(http.MethodGet, "/api/tls/check"+tc.query, nil)
			if code := do(srv, r).Code; code != tc.want {
				t.Errorf("%q: %d, want %d", tc.query, code, tc.want)
			}
		})
	}
}

// --- Operator recovery of a hostname ---

// adminOn seeds an administrator on the alias fixture and returns a request
// builder authenticated as them.
func adminOn(t *testing.T, database *dbpkg.DB) func(method, path string) *http.Request {
	t.Helper()
	if err := database.CreateUser(&dbpkg.User{ID: "admin1", Email: "admin@example.com", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	sess, err := database.CreateSession("admin1")
	if err != nil {
		t.Fatal(err)
	}
	return func(method, path string) *http.Request {
		r := httptest.NewRequest(method, path, nil)
		r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sess.Token})
		return r
	}
}

// Verification proves a hostname points at this gateway, not who controls it,
// so the first account to verify a name keeps it. This is the operator's way to
// undo that without deleting the holder's VM or their whole account.
func TestAdminReleasesAHostname(t *testing.T) {
	srv, database, verifier := newAliasTestServer(t)
	asAdmin := adminOn(t, database)

	// vm1 holds the name; vm2 has a pending claim on it from before.
	parked := decodeAlias(t, addAlias(t, srv, "vm2", "app.example.org"))
	holder := decodeAlias(t, addAlias(t, srv, "vm1", "app.example.org"))
	verifier.accept["app.example.org"] = true
	if got := decodeAlias(t, do(srv, authedRequest(http.MethodPost, "/api/containers/vm1/aliases/"+holder.ID+"/verify", nil))); !got.Verified {
		t.Fatalf("holder did not verify: %q", got.LastError)
	}
	_ = parked

	if code := do(srv, asAdmin(http.MethodDelete, "/api/admin/aliases/app.example.org")).Code; code != http.StatusNoContent {
		t.Fatalf("release: %d, want 204", code)
	}

	// The name is now unclaimed: it routes nowhere, gets no certificate, and
	// the account that should have it can take it.
	if _, err := database.GetVerifiedAliasByHostname("app.example.org"); err == nil {
		t.Error("a released hostname still routes")
	}
	if code := do(srv, httptest.NewRequest(http.MethodGet, "/api/tls/check?domain=app.example.org", nil)).Code; code != http.StatusNotFound {
		t.Errorf("a released hostname still gets a certificate: %d", code)
	}
	if left, _ := database.ListAliasesByContainer("vm1"); len(left) != 0 {
		t.Errorf("the holder's claim survived the release: %+v", left)
	}
	if code := addAlias(t, srv, "vm2", "app.example.org").Code; code != http.StatusCreated {
		t.Errorf("a released hostname must be claimable again: %d", code)
	}
}

// Releasing one name must not disturb any other.
func TestAdminReleaseTouchesOnlyThatHostname(t *testing.T) {
	srv, database, verifier := newAliasTestServer(t)
	asAdmin := adminOn(t, database)
	verifier.accept["app.example.org"] = true
	verifier.accept["other.example.org"] = true

	addAlias(t, srv, "vm1", "app.example.org")
	keep := decodeAlias(t, addAlias(t, srv, "vm1", "other.example.org"))

	if code := do(srv, asAdmin(http.MethodDelete, "/api/admin/aliases/APP.Example.org.")).Code; code != http.StatusNoContent {
		t.Fatalf("release with a differently-cased hostname: %d, want 204", code)
	}
	got, err := database.GetAliasByID(keep.ID)
	if err != nil || !got.Verified {
		t.Errorf("an unrelated domain was disturbed: %+v, %v", got, err)
	}
}

func TestAdminReleaseUnknownHostname(t *testing.T) {
	srv, database, _ := newAliasTestServer(t)
	asAdmin := adminOn(t, database)

	if code := do(srv, asAdmin(http.MethodDelete, "/api/admin/aliases/nobody.example.org")).Code; code != http.StatusNotFound {
		t.Errorf("releasing an unclaimed hostname: %d, want 404", code)
	}
}

// Freeing somebody else's domain is an operator power, not a tenant one.
func TestAdminAliasRoutesRejectNonAdmins(t *testing.T) {
	srv, database, verifier := newAliasTestServer(t)
	adminOn(t, database)
	verifier.accept["app.example.org"] = true
	addAlias(t, srv, "vm1", "app.example.org")

	for _, r := range []*http.Request{
		authedRequest(http.MethodGet, "/api/admin/aliases", nil),
		authedRequest(http.MethodDelete, "/api/admin/aliases/app.example.org", nil),
	} {
		if code := do(srv, r).Code; code != http.StatusForbidden {
			t.Errorf("%s %s as a plain user: %d, want 403", r.Method, r.URL.Path, code)
		}
	}
	// And it really is still there.
	if _, err := database.GetVerifiedAliasByHostname("app.example.org"); err != nil {
		t.Errorf("a non-admin request removed the hostname: %v", err)
	}

	// Anonymous callers get no further.
	if code := do(srv, httptest.NewRequest(http.MethodDelete, "/api/admin/aliases/app.example.org", nil)).Code; code != http.StatusUnauthorized {
		t.Errorf("anonymous release: %d, want 401", code)
	}
}

// The operator has to see who holds a name before deciding to take it away.
func TestAdminListAliasesNamesTheHolder(t *testing.T) {
	srv, database, verifier := newAliasTestServer(t)
	asAdmin := adminOn(t, database)
	verifier.accept["app.example.org"] = true
	addAlias(t, srv, "vm1", "app.example.org")

	w := do(srv, asAdmin(http.MethodGet, "/api/admin/aliases"))
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var list []*dbpkg.AliasWithOwner
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 alias, got %d", len(list))
	}
	got := list[0]
	if got.Hostname != "app.example.org" || got.ContainerName != "box" || got.OwnerEmail != "user1@example.com" {
		t.Errorf("the list does not identify the holder: %+v", got)
	}
	if !got.Verified {
		t.Error("verified state is not reported")
	}
	_ = database
}
