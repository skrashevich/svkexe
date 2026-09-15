package metadata

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"
)

// clock is a hand-wound time source, so token expiry is tested by moving time
// rather than by waiting for it.
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func tokenService(t *testing.T, ids map[string]*Identity, now func() time.Time) *Service {
	t.Helper()
	return New(&fakeResolver{byAddr: ids}, Config{Domain: "svk.exe", Now: now})
}

func issue(t *testing.T, srv http.Handler, remote, ttl string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPut, tokenPath, nil)
	r.RemoteAddr = remote + ":41234"
	if ttl != "" {
		r.Header.Set(tokenTTLHeader, ttl)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

func TestTokenIssueEchoesTheRequestedTTL(t *testing.T) {
	srv := testService(t, sampleIdentity())

	w := issue(t, srv, callerIP, "60")
	if w.Code != http.StatusOK {
		t.Fatalf("PUT %s: want 200, got %d (%q)", tokenPath, w.Code, w.Body.String())
	}
	if w.Body.Len() == 0 {
		t.Error("the token endpoint returned an empty token")
	}
	if got := w.Header().Get(tokenTTLHeader); got != "60" {
		t.Errorf("%s: want 60, got %q", tokenTTLHeader, got)
	}
}

func TestTokenEndpointRejectsWrongMethodAndTTL(t *testing.T) {
	srv := testService(t, sampleIdentity())

	w := do(t, srv, http.MethodGet, tokenPath, nil)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET on the token endpoint: want 405, got %d", w.Code)
	}
	if allow := w.Header().Get("Allow"); allow != "PUT" {
		t.Errorf("Allow: want PUT, got %q", allow)
	}

	for _, ttl := range []string{"", "soon", "0", strconv.Itoa(maxTokenTTL + 1), "-1"} {
		if w := issue(t, srv, callerIP, ttl); w.Code != http.StatusBadRequest {
			t.Errorf("TTL %q: want 400, got %d", ttl, w.Code)
		}
	}
	for _, ttl := range []string{strconv.Itoa(minTokenTTL), strconv.Itoa(maxTokenTTL)} {
		if w := issue(t, srv, callerIP, ttl); w.Code != http.StatusOK {
			t.Errorf("TTL %q: want 200, got %d", ttl, w.Code)
		}
	}
}

func TestDataRequestWithAToken(t *testing.T) {
	srv := testService(t, sampleIdentity())
	token := issue(t, srv, callerIP, "60").Body.String()

	w := do(t, srv, http.MethodGet, "/latest/meta-data/instance-id", map[string]string{tokenHeader: token})
	if w.Code != http.StatusOK || w.Body.String() != "c-1" {
		t.Fatalf("with a valid token: status %d, body %q", w.Code, w.Body.String())
	}

	w = do(t, srv, http.MethodGet, "/latest/meta-data/instance-id", map[string]string{tokenHeader: "not-a-token"})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("with an unknown token: want 401, got %d", w.Code)
	}
}

// IMDSv1 must keep answering: tooling that predates the token flow is exactly
// what a compatibility endpoint exists for.
func TestTokenIsOptional(t *testing.T) {
	srv := testService(t, sampleIdentity())
	if w := get(t, srv, "/latest/meta-data/instance-id"); w.Code != http.StatusOK {
		t.Errorf("without a token: want 200, got %d", w.Code)
	}
}

// One service answers every VM on the host, so a token has to prove which VM
// asked for it. Otherwise a VM that gets hold of a neighbour's token reads the
// neighbour's metadata.
func TestATokenIsBoundToTheVMItWasIssuedTo(t *testing.T) {
	const otherIP = "10.100.0.6"
	mine := sampleIdentity()
	theirs := sampleIdentity()
	theirs.ContainerID = "c-2"
	theirs.Name = "other"
	theirs.IPv4 = otherIP

	srv := tokenService(t, map[string]*Identity{callerIP: mine, otherIP: theirs}, nil)

	theirToken := issue(t, srv, otherIP, "60").Body.String()
	if theirToken == "" {
		t.Fatal("no token issued for the other VM")
	}

	r := httptest.NewRequest(http.MethodGet, "/latest/meta-data/instance-id", nil)
	r.RemoteAddr = callerIP + ":41234"
	r.Header.Set(tokenHeader, theirToken)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("presenting another VM's token: want 401, got %d (%q)", w.Code, w.Body.String())
	}
}

func TestTokenExpires(t *testing.T) {
	c := &clock{t: time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)}
	srv := tokenService(t, map[string]*Identity{callerIP: sampleIdentity()}, c.now)

	token := issue(t, srv, callerIP, "60").Body.String()
	c.add(59 * time.Second)
	if w := do(t, srv, http.MethodGet, "/latest/meta-data/instance-id", map[string]string{tokenHeader: token}); w.Code != http.StatusOK {
		t.Errorf("one second before expiry: want 200, got %d", w.Code)
	}
	c.add(2 * time.Second)
	if w := do(t, srv, http.MethodGet, "/latest/meta-data/instance-id", map[string]string{tokenHeader: token}); w.Code != http.StatusUnauthorized {
		t.Errorf("after expiry: want 401, got %d", w.Code)
	}
}

// Talking something inside the VM into fetching this address on an attacker's
// behalf is the classic way instance credentials leak, and such a request
// carries X-Forwarded-For. EC2 refuses it on the token endpoint; refusing it
// everywhere costs nothing legitimate.
func TestProxiedRequestsAreRefused(t *testing.T) {
	srv := testService(t, sampleIdentity())

	for _, path := range []string{tokenPath, "/latest/meta-data/instance-id", "/latest/meta-data/"} {
		method := http.MethodGet
		if path == tokenPath {
			method = http.MethodPut
		}
		w := do(t, srv, method, path, map[string]string{
			"X-Forwarded-For":  "198.51.100.4",
			tokenTTLHeader:     "60",
			"X-Requested-With": "curl",
		})
		if w.Code != http.StatusMisdirectedRequest {
			t.Errorf("%s %s with X-Forwarded-For: want 421, got %d", method, path, w.Code)
		}
		if b := w.Body.String(); strings.Contains(b, "c-1") || strings.Contains(b, "svkexe-owner-box") {
			t.Errorf("%s %s leaked metadata in the refusal: %q", method, path, b)
		}
	}
}

// A doubled separator is still the token endpoint. Path normalisation has to
// happen before the endpoint is recognised, or a caller could sidestep the token
// rules by spelling the path differently.
func TestTokenPathIgnoresRedundantSeparators(t *testing.T) {
	srv := testService(t, sampleIdentity())
	r := httptest.NewRequest(http.MethodPut, "/latest//api/token", nil)
	r.RemoteAddr = callerIP + ":41234"
	r.Header.Set(tokenTTLHeader, "60")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("a doubled separator should still reach the token endpoint: got %d", w.Code)
	}
}

// A token proves which VM asked for it and when it stops being valid, and
// nothing is kept on the gateway to prove it with. That is what makes the
// endpoint safe to leave unlimited in one respect: a VM looping token requests
// cannot grow anything on the host.
func TestTokensAreStateless(t *testing.T) {
	c := &clock{t: time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)}
	store := newTokenStore(c.now)

	issued := map[string]bool{}
	for range 2000 {
		token, err := store.issue("c-1", time.Duration(maxTokenTTL)*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		issued[token] = true
	}
	// Every one still verifies — there is no capacity to have evicted them from.
	for token := range issued {
		if !store.valid(token, "c-1") {
			t.Fatal("a token stopped verifying, so something is being kept and forgotten")
		}
		if store.valid(token, "c-2") {
			t.Fatal("a token verified for a VM it was not issued to")
		}
	}
	if len(issued) != 2000 {
		t.Errorf("tokens collided: %d distinct out of 2000", len(issued))
	}
}

// Forging one is the only way past the container binding, so the signature has
// to be checked before anything in the token is believed.
func TestATamperedTokenIsRejected(t *testing.T) {
	c := &clock{t: time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)}
	store := newTokenStore(c.now)
	token, err := store.issue("c-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	payload, signature, _ := cutLast(token, ".")
	expiry, container, _ := strings.Cut(payload, ".")

	// A later expiry, keeping the original signature.
	extended := "99999999999." + container + "." + signature
	if store.valid(extended, "c-1") {
		t.Error("a token with a rewritten expiry was accepted")
	}
	// Another VM's id, keeping the original signature.
	swapped := expiry + "." + base64.RawURLEncoding.EncodeToString([]byte("c-2")) + "." + signature
	if store.valid(swapped, "c-2") {
		t.Error("a token with a rewritten container was accepted")
	}
	// A token from a different process — a restart, or another gateway.
	other := newTokenStore(c.now)
	if other.valid(token, "c-1") {
		t.Error("a token verified against a key that never issued it")
	}
	for _, junk := range []string{"", ".", "not-a-token", payload, payload + ".x"} {
		if store.valid(junk, "c-1") {
			t.Errorf("malformed token %q was accepted", junk)
		}
	}
}

// countingLimiter refuses everything after a fixed number of calls.
type countingLimiter struct {
	allowed int
	calls   int
}

func (l *countingLimiter) Allow(string) bool {
	l.calls++
	return l.calls <= l.allowed
}

// Nothing here authenticates, so there is no account to hold responsible for a
// loop inside a VM — and every answer costs a read on the pool the dashboard,
// the API and the SSH gateway share.
func TestARunawayCallerIsThrottled(t *testing.T) {
	limiter := &countingLimiter{allowed: 2}
	srv := New(&fakeResolver{byAddr: map[string]*Identity{callerIP: sampleIdentity()}},
		Config{Domain: "svk.exe", Limiter: limiter})

	for i := range 2 {
		if w := get(t, srv, "/latest/meta-data/instance-id"); w.Code != http.StatusOK {
			t.Fatalf("request %d: want 200, got %d", i, w.Code)
		}
	}
	w := get(t, srv, "/latest/meta-data/instance-id")
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("past the limit: want 429, got %d", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("a throttled caller is not told when to come back")
	}
	if strings.Contains(w.Body.String(), "c-1") {
		t.Error("the refusal leaked metadata")
	}
}

// The limit has to apply before the caller is resolved, or a flood from an
// address that belongs to nothing still costs a lookup per request.
func TestThrottlingHappensBeforeResolution(t *testing.T) {
	limiter := &countingLimiter{allowed: 0}
	resolver := &countingResolver{}
	srv := New(resolver, Config{Limiter: limiter})

	for range 10 {
		if w := get(t, srv, "/latest/meta-data/"); w.Code != http.StatusTooManyRequests {
			t.Fatalf("want 429, got %d", w.Code)
		}
	}
	if resolver.calls != 0 {
		t.Errorf("the resolver was consulted %d times for requests that were refused anyway", resolver.calls)
	}
}

type countingResolver struct{ calls int }

func (r *countingResolver) Resolve(context.Context, netip.Addr) (*Identity, error) {
	r.calls++
	return sampleIdentity(), nil
}
