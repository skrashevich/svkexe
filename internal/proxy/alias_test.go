package proxy

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/skrashevich/svkexe/internal/db"
)

// verifiedAlias claims a hostname for the fixture's VM and marks it verified,
// which is the only state routing looks at.
func verifiedAlias(t *testing.T, f *routingFixture, hostname string) *db.ContainerAlias {
	t.Helper()
	a, err := f.database.CreateContainerAlias("vm", hostname)
	if err != nil {
		t.Fatalf("CreateContainerAlias(%s): %v", hostname, err)
	}
	if err := f.database.SetAliasVerification(a.ID, true, ""); err != nil {
		t.Fatalf("SetAliasVerification: %v", err)
	}
	return a
}

func publish(t *testing.T, f *routingFixture, public bool) int {
	t.Helper()
	c, err := f.database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.database.UpdateContainerPublish("vm", c.AppPort, public); err != nil {
		t.Fatal(err)
	}
	return c.AppPort
}

// assertServedFromAppPort proves the gateway proxied to the workload port. The
// fixture backend listens on exactly that port, so a 200 could not have come
// from anywhere else — and the recorded host says so explicitly.
func assertServedFromAppPort(t *testing.T, f *routingFixture, appPort int) {
	t.Helper()
	if f.lastHost == "" {
		t.Fatal("workload was not reached")
	}
	_, port, ok := strings.Cut(f.lastHost, ":")
	if !ok {
		t.Fatalf("unexpected upstream host %q", f.lastHost)
	}
	got, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("unexpected upstream port in %q: %v", f.lastHost, err)
	}
	if got != appPort {
		t.Errorf("proxied to port %d, want the workload port %d", got, appPort)
	}
	if got == db.AgentPort {
		t.Errorf("an alias reached the agent port %d", db.AgentPort)
	}
}

func TestAliasServesPublishedWorkload(t *testing.T) {
	f := newRoutingFixture(t)
	appPort := publish(t, f, true)
	verifiedAlias(t, f, "app.example.org")

	w := f.get(t, "app.example.org", "")
	if w.Code != http.StatusOK {
		t.Fatalf("alias request: %d, want 200", w.Code)
	}
	assertServedFromAppPort(t, f, appPort)
	// The workload authenticates its own users; the gateway asserts no identity
	// on a domain it does not own the cookies for.
	f.assertNoIdentityLeak(t)
}

// A Host header arrives in whatever case the client typed, and an FQDN may
// carry a trailing dot. Both name the certificate's hostname.
func TestAliasHostIsNormalised(t *testing.T) {
	f := newRoutingFixture(t)
	publish(t, f, true)
	verifiedAlias(t, f, "app.example.org")

	for _, host := range []string{"APP.Example.ORG", "app.example.org.", "app.example.org:8443"} {
		if code := f.get(t, host, "").Code; code != http.StatusOK {
			t.Errorf("alias request to %q: %d, want 200", host, code)
		}
	}
}

// The session cookie is scoped to the gateway's own domain and never reaches a
// custom one, so a private workload has no way to authenticate its owner here.
// Saying that plainly beats a 401 the owner can never satisfy.
func TestAliasRefusesUnpublishedWorkload(t *testing.T) {
	f := newRoutingFixture(t)
	publish(t, f, false)
	verifiedAlias(t, f, "app.example.org")

	w := f.get(t, "app.example.org", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("private workload over an alias: %d, want 403", w.Code)
	}
	if f.lastHost != "" {
		t.Error("the request was proxied to the workload despite being refused")
	}
	if !strings.Contains(w.Body.String(), "publish") {
		t.Errorf("the refusal must tell the owner how to fix it, got %q", w.Body.String())
	}

	// Even the owner's own session must not open it: the cookie is not sent to
	// this domain in the first place, so honouring one here would only reward a
	// client that forged it.
	if code := f.get(t, "app.example.org", f.sessionID).Code; code != http.StatusForbidden {
		t.Errorf("session cookie on an alias host: %d, want 403", code)
	}
}

func TestAliasHonoursShareToken(t *testing.T) {
	f := newRoutingFixture(t)
	appPort := publish(t, f, false)
	verifiedAlias(t, f, "app.example.org")

	link, err := f.database.CreateSharedLink("vm", "owner", nil)
	if err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "https://app.example.org/?share="+link.Token, nil)
	w := httptest.NewRecorder()
	f.lastHost = ""
	f.proxy.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("shared alias request: %d, want 200", w.Code)
	}
	assertServedFromAppPort(t, f, appPort)
	if r.URL.Query().Has("share") {
		t.Error("share token forwarded to the workload URL")
	}

	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != shareCookieName {
		t.Fatalf("no share cookie was set: %+v", cookies)
	}

	// The cookie carries the grant to the page's later requests.
	next := httptest.NewRequest(http.MethodGet, "https://app.example.org/assets/app.js", nil)
	next.AddCookie(cookies[0])
	out := httptest.NewRecorder()
	f.proxy.ServeHTTP(out, next)
	if out.Code != http.StatusOK {
		t.Fatalf("share cookie on an alias host: %d, want 200", out.Code)
	}

	// Revoking the link closes the alias immediately.
	if err := f.database.DeleteSharedLinkByToken(link.Token); err != nil {
		t.Fatal(err)
	}
	revoked := httptest.NewRequest(http.MethodGet, "https://app.example.org/", nil)
	revoked.AddCookie(cookies[0])
	out = httptest.NewRecorder()
	f.proxy.ServeHTTP(out, revoked)
	if out.Code != http.StatusForbidden {
		t.Fatalf("revoked share on an alias host: %d, want 403", out.Code)
	}
}

// A share for a different VM must not open this one just because it arrived on
// its hostname.
func TestAliasRejectsForeignShareToken(t *testing.T) {
	f := newRoutingFixture(t)
	publish(t, f, false)
	verifiedAlias(t, f, "app.example.org")

	if err := f.database.CreateContainer(&db.Container{
		ID: "other", Name: "other", OwnerID: "intruder", IncusName: "other", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	link, err := f.database.CreateSharedLink("other", "intruder", nil)
	if err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "https://app.example.org/?share="+link.Token, nil)
	w := httptest.NewRecorder()
	f.lastHost = ""
	f.proxy.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("another VM's share on this alias: %d, want 403", w.Code)
	}
	if f.lastHost != "" {
		t.Error("a foreign share token reached the workload")
	}
}

// Nothing is routed until a DNS check confirms the hostname points here;
// otherwise anyone could claim a name they do not control.
func TestUnverifiedAliasIsNotRoutable(t *testing.T) {
	f := newRoutingFixture(t)
	publish(t, f, true)

	a, err := f.database.CreateContainerAlias("vm", "app.example.org")
	if err != nil {
		t.Fatal(err)
	}

	if code := f.get(t, "app.example.org", "").Code; code != http.StatusNotFound {
		t.Errorf("unverified alias: %d, want 404", code)
	}
	if f.lastHost != "" {
		t.Error("an unverified alias reached the workload")
	}

	// Verifying it opens the route; a later failed re-check closes it again.
	if err := f.database.SetAliasVerification(a.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if code := f.get(t, "app.example.org", "").Code; code != http.StatusOK {
		t.Errorf("verified alias: %d, want 200", code)
	}
	if err := f.database.SetAliasVerification(a.ID, false, "no longer points here"); err != nil {
		t.Fatal(err)
	}
	if code := f.get(t, "app.example.org", "").Code; code != http.StatusNotFound {
		t.Errorf("alias after a failed re-check: %d, want 404", code)
	}
}

func TestUnknownAliasHostIsNotFound(t *testing.T) {
	f := newRoutingFixture(t)
	publish(t, f, true)

	if code := f.get(t, "nobody.example.org", "").Code; code != http.StatusNotFound {
		t.Errorf("unclaimed hostname: %d, want 404", code)
	}
	// A malformed name inside the gateway's own domain is a broken VM host, not
	// a custom domain, and must keep saying so.
	if code := f.get(t, "a.b.c.example.com", "").Code; code != http.StatusBadRequest {
		t.Errorf("malformed VM host: %d, want 400", code)
	}
}

// KnowsHost is what decides whether a request reaches the proxy at all, so
// ServeHTTP must serve every host KnowsHost claims. A gateway without DOMAIN
// has no subdomain namespace to protect and used to answer those with 400.
func TestAliasServedWithoutAConfiguredDomain(t *testing.T) {
	f := newRoutingFixture(t)
	publish(t, f, true)
	verifiedAlias(t, f, "app.example.org")
	// Aliases still verify without DOMAIN when the operator names the
	// gateway's addresses through GATEWAY_PUBLIC_IPS.
	f.proxy = New(f.database, nil, "")

	if !f.proxy.KnowsHost("app.example.org") {
		t.Fatal("KnowsHost rejected a verified alias")
	}
	if code := f.get(t, "app.example.org", "").Code; code != http.StatusOK {
		t.Errorf("alias without a configured domain: %d, want 200", code)
	}
	if code := f.get(t, "nobody.example.org", "").Code; code != http.StatusNotFound {
		t.Errorf("unclaimed hostname without a configured domain: %d, want 404", code)
	}
}

// An alias never carries a gateway-asserted identity, even when the caller
// forges the header the agent trusts.
func TestAliasStripsIdentityHeaders(t *testing.T) {
	f := newRoutingFixture(t)
	publish(t, f, true)
	verifiedAlias(t, f, "app.example.org")

	r := httptest.NewRequest(http.MethodGet, "https://app.example.org/", nil)
	r.Header.Set("X-ExeDev-Userid", "spoofed")
	r.Header.Set("X-ExeDev-Email", "spoofed@example.com")
	w := httptest.NewRecorder()
	f.proxy.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("alias request: %d, want 200", w.Code)
	}
	f.assertNoIdentityLeak(t)
	if got := r.Header.Get("X-ExeDev-Userid"); got != "" {
		t.Errorf("identity header survived on the request: %q", got)
	}
}

func TestKnowsHost(t *testing.T) {
	f := newRoutingFixture(t)
	publish(t, f, true)
	unverified, err := f.database.CreateContainerAlias("vm", "pending.example.org")
	if err != nil {
		t.Fatal(err)
	}
	verifiedAlias(t, f, "app.example.org")

	cases := []struct {
		host string
		want bool
	}{
		{"app.example.org", true},
		{"APP.Example.org.", true},
		{"app.example.org:8443", true},
		{"pending.example.org", false},
		{"nobody.example.org", false},
		{"", false},
		// The gateway's own namespace is routed by subdomain, never by alias.
		{"example.com", false},
		{"box.example.com", false},
	}
	for _, tc := range cases {
		if got := f.proxy.KnowsHost(tc.host); got != tc.want {
			t.Errorf("KnowsHost(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}

	if err := f.database.SetAliasVerification(unverified.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if !f.proxy.KnowsHost("pending.example.org") {
		t.Error("KnowsHost must see an alias as soon as it verifies")
	}
}
