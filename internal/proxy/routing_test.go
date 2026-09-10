package proxy

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/skrashevich/svkexe/internal/db"
)

func TestExtractSubdomainRoutes(t *testing.T) {
	p := &ContainerProxy{domain: "example.com"}

	cases := []struct {
		host     string
		wantName string
		wantKind routeKind
		wantPort int
		ok       bool
	}{
		// The bare VM host is the user's workload, on the VM's configured port.
		{"mybox.example.com", "mybox", routeApp, 0, true},
		{"mybox.example.com:8080", "mybox", routeApp, 0, true},
		// An explicit port opens any other listener in the VM.
		{"3000-mybox.example.com", "mybox", routeApp, 3000, true},
		{"8080-my-box.example.com", "my-box", routeApp, 8080, true},
		// The agent lives on its own single-label host so the wildcard cert covers it.
		{"agent-mybox.example.com", "mybox", routeAgent, 0, true},
		{"agent-my-box.example.com", "my-box", routeAgent, 0, true},
		// Legacy service-prefixed links keep resolving to the agent.
		{"picoclaw.mybox.example.com", "mybox", routeAgent, 0, true},
		{"shelley.mybox.example.com", "mybox", routeAgent, 0, true},
		{"unknown.mybox.example.com", "", routeApp, 0, false},
		// Malformed hosts.
		{"example.com", "", routeApp, 0, false},
		{"other.domain.com", "", routeApp, 0, false},
		{"a.b.c.example.com", "", routeApp, 0, false},
		{"", "", routeApp, 0, false},
		{".example.com", "", routeApp, 0, false},
		{"agent-.example.com", "", routeApp, 0, false},
		{"3000-.example.com", "", routeApp, 0, false},
		{"0-mybox.example.com", "", routeApp, 0, false},
		{"99999-mybox.example.com", "", routeApp, 0, false},
		{"UPPER.example.com", "", routeApp, 0, false},
	}

	for _, tc := range cases {
		got, ok := p.extractSubdomain(tc.host)
		if ok != tc.ok || (ok && (got.ContainerName != tc.wantName || got.Kind != tc.wantKind || got.Port != tc.wantPort)) {
			t.Errorf("extractSubdomain(%q) = (%+v, %v), want (name=%q kind=%v port=%d, %v)",
				tc.host, got, ok, tc.wantName, tc.wantKind, tc.wantPort, tc.ok)
		}
	}
}

// A VM literally named "agent-foo" must not shadow the agent host of VM "foo",
// which is why those names are rejected at creation time.
func TestReservedContainerNames(t *testing.T) {
	for _, name := range []string{"agent-foo", "3000-foo", "0-foo"} {
		if !db.ReservedName(name) {
			t.Errorf("%q must be reserved", name)
		}
	}
	for _, name := range []string{"foo", "my-box", "agentfoo", "foo-3000", "a3000-foo"} {
		if db.ReservedName(name) {
			t.Errorf("%q must be allowed", name)
		}
	}
}

type routingFixture struct {
	proxy   *ContainerProxy
	backend *httptest.Server
	// lastHeaders records what the workload actually received.
	lastHeaders http.Header
	// lastHost records the address the request was proxied to, which is how a
	// test can tell which in-VM port the gateway picked.
	lastHost  string
	database  *db.DB
	sessionID string
}

func newRoutingFixture(t *testing.T) *routingFixture {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if _, err := database.EnsureUser("owner", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.EnsureUser("intruder", "intruder@example.com"); err != nil {
		t.Fatal(err)
	}
	f := &routingFixture{database: database}
	f.backend = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastHeaders = r.Header.Clone()
		f.lastHost = r.Host
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(f.backend.Close)
	host, port, ok := strings.Cut(f.backend.Listener.Addr().String(), ":")
	if !ok {
		t.Fatalf("unexpected backend address %q", f.backend.Listener.Addr())
	}
	if err := database.CreateContainer(&db.Container{
		ID: "vm", Name: "box", OwnerID: "owner", IncusName: "box",
		Status: "running", IPAddress: host,
	}); err != nil {
		t.Fatal(err)
	}
	// Point the VM's app port at the fake backend so a successful route returns 200.
	appPort, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateContainerPublish("vm", appPort, false); err != nil {
		t.Fatal(err)
	}
	session, err := database.CreateSession("owner")
	if err != nil {
		t.Fatal(err)
	}
	f.sessionID = session.Token
	f.proxy = New(database, nil, "example.com")
	return f
}

func (f *routingFixture) get(t *testing.T, host string, session string) *httptest.ResponseRecorder {
	t.Helper()
	f.lastHeaders = nil
	f.lastHost = ""
	r := httptest.NewRequest(http.MethodGet, "https://"+host+"/", nil)
	if session != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	}
	w := httptest.NewRecorder()
	f.proxy.ServeHTTP(w, r)
	return w
}

// The workload must never receive a gateway-asserted identity: it is either
// public or authenticates its own users.
func (f *routingFixture) assertNoIdentityLeak(t *testing.T) {
	t.Helper()
	if f.lastHeaders == nil {
		t.Fatal("workload was not reached")
	}
	for _, header := range []string{"X-ExeDev-Userid", "X-ExeDev-Email"} {
		if got := f.lastHeaders.Get(header); got != "" {
			t.Errorf("workload received %s=%q", header, got)
		}
	}
}

func TestPrivateWorkloadRequiresSession(t *testing.T) {
	f := newRoutingFixture(t)

	if code := f.get(t, "box.example.com", "").Code; code != http.StatusUnauthorized {
		t.Errorf("anonymous access to a private workload: %d, want 401", code)
	}
	if code := f.get(t, "box.example.com", f.sessionID).Code; code != http.StatusOK {
		t.Errorf("owner access: %d, want 200", code)
	}
	f.assertNoIdentityLeak(t)
}

func TestPublicWorkloadServesAnonymously(t *testing.T) {
	f := newRoutingFixture(t)
	c, err := f.database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.database.UpdateContainerPublish("vm", c.AppPort, true); err != nil {
		t.Fatal(err)
	}

	if code := f.get(t, "box.example.com", "").Code; code != http.StatusOK {
		t.Errorf("anonymous access to a public workload: %d, want 200", code)
	}
	f.assertNoIdentityLeak(t)

	// Flipping back to private closes access without a restart.
	if err := f.database.UpdateContainerPublish("vm", c.AppPort, false); err != nil {
		t.Fatal(err)
	}
	if code := f.get(t, "box.example.com", "").Code; code != http.StatusUnauthorized {
		t.Errorf("public -> private did not close access: %d, want 401", code)
	}
}

// Publishing a workload must not publish the agent, which can run commands and
// read the whole container.
func TestAgentNeverPublic(t *testing.T) {
	f := newRoutingFixture(t)
	c, err := f.database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.database.UpdateContainerPublish("vm", c.AppPort, true); err != nil {
		t.Fatal(err)
	}

	for _, host := range []string{"agent-box.example.com", "picoclaw.box.example.com"} {
		if code := f.get(t, host, "").Code; code != http.StatusUnauthorized {
			t.Errorf("anonymous access to %s: %d, want 401", host, code)
		}
	}
}

// The flag publishes exactly the configured port, not everything listening.
func TestExplicitPortHostNeverPublic(t *testing.T) {
	f := newRoutingFixture(t)
	c, err := f.database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.database.UpdateContainerPublish("vm", c.AppPort, true); err != nil {
		t.Fatal(err)
	}

	host := "3000-box.example.com"
	if code := f.get(t, host, "").Code; code != http.StatusUnauthorized {
		t.Errorf("anonymous access to %s: %d, want 401", host, code)
	}
}

func TestWorkloadOwnershipEnforced(t *testing.T) {
	f := newRoutingFixture(t)
	session, err := f.database.CreateSession("intruder")
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"box.example.com", "agent-box.example.com", "3000-box.example.com"} {
		if code := f.get(t, host, session.Token).Code; code != http.StatusNotFound {
			t.Errorf("intruder reached %s: %d, want 404", host, code)
		}
	}
}

// Nothing listening on the port is the user's own misconfiguration, and the
// message has to say so instead of looking like a gateway failure.
func TestUnreachableWorkloadReportsBadGateway(t *testing.T) {
	f := newRoutingFixture(t)
	f.backend.Close()

	w := f.get(t, "box.example.com", f.sessionID)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502", w.Code)
	}
	if !strings.Contains(w.Body.String(), "nothing is listening") {
		t.Errorf("unhelpful body: %q", w.Body.String())
	}
}
