package dnscheck

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stubResolver builds a lookupIP func from a fixed host→IPs map and counts
// how many times each host is looked up, so tests can assert on caching
// behaviour without touching the real network.
type stubResolver struct {
	answers map[string][]net.IP
	fail    map[string]error
	hits    map[string]*atomic.Int64
}

func newStubResolver() *stubResolver {
	return &stubResolver{
		answers: map[string][]net.IP{},
		fail:    map[string]error{},
		hits:    map[string]*atomic.Int64{},
	}
}

func (s *stubResolver) lookup(_ context.Context, host string) ([]net.IP, error) {
	counter, ok := s.hits[host]
	if !ok {
		counter = &atomic.Int64{}
		s.hits[host] = counter
	}
	counter.Add(1)

	if err, ok := s.fail[host]; ok {
		return nil, err
	}
	return s.answers[host], nil
}

func (s *stubResolver) hitsFor(host string) int64 {
	c, ok := s.hits[host]
	if !ok {
		return 0
	}
	return c.Load()
}

func ips(addrs ...string) []net.IP {
	out := make([]net.IP, len(addrs))
	for i, a := range addrs {
		out[i] = net.ParseIP(a)
	}
	return out
}

func TestVerify_SharedAddress(t *testing.T) {
	r := newStubResolver()
	r.answers["app.example.org"] = ips("203.0.113.9")
	r.answers["example.com"] = ips("203.0.113.9")

	v := New("example.com", nil)
	v.lookupIP = r.lookup

	if err := v.Verify(context.Background(), "app.example.org"); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestVerify_AliasResolvesElsewhere(t *testing.T) {
	r := newStubResolver()
	r.answers["app.example.org"] = ips("203.0.113.9")
	r.answers["example.com"] = ips("198.51.100.4")

	v := New("example.com", nil)
	v.lookupIP = r.lookup

	err := v.Verify(context.Background(), "app.example.org")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "203.0.113.9") || !strings.Contains(err.Error(), "198.51.100.4") {
		t.Fatalf("error should name both address sets, got: %v", err)
	}
	if !strings.Contains(err.Error(), "app.example.org") || !strings.Contains(err.Error(), "example.com") {
		t.Fatalf("error should name both hostnames, got: %v", err)
	}
}

func TestVerify_AliasLookupFails(t *testing.T) {
	r := newStubResolver()
	r.answers["example.com"] = ips("203.0.113.9")
	r.fail["app.example.org"] = errors.New("no such host")

	v := New("example.com", nil)
	v.lookupIP = r.lookup

	err := v.Verify(context.Background(), "app.example.org")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "does not resolve yet") {
		t.Fatalf("error should mention that DNS may still be propagating, got: %v", err)
	}
}

func TestVerify_StaticIPsOverrideAndSkipDomainLookup(t *testing.T) {
	r := newStubResolver()
	r.answers["app.example.org"] = ips("198.51.100.4")

	v := New("example.com", []string{"198.51.100.4"})
	v.lookupIP = r.lookup

	if err := v.Verify(context.Background(), "app.example.org"); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if hits := r.hitsFor("example.com"); hits != 0 {
		t.Fatalf("expected DOMAIN to never be resolved when GATEWAY_PUBLIC_IPS is set, got %d lookups", hits)
	}
}

func TestVerify_GatewayAddressesAreCached(t *testing.T) {
	r := newStubResolver()
	r.answers["example.com"] = ips("203.0.113.9")
	r.answers["one.example.org"] = ips("203.0.113.9")
	r.answers["two.example.org"] = ips("203.0.113.9")

	v := New("example.com", nil)
	v.lookupIP = r.lookup

	if err := v.Verify(context.Background(), "one.example.org"); err != nil {
		t.Fatalf("first Verify: expected nil error, got %v", err)
	}
	if err := v.Verify(context.Background(), "two.example.org"); err != nil {
		t.Fatalf("second Verify: expected nil error, got %v", err)
	}

	if hits := r.hitsFor("example.com"); hits != 1 {
		t.Fatalf("expected exactly one lookup of DOMAIN across two Verify calls, got %d", hits)
	}
}

func TestVerify_IPv4InIPv6RepresentationsMatch(t *testing.T) {
	r := newStubResolver()
	// The gateway's own address comes back as an IPv4-in-IPv6 form (as some
	// resolvers do for "ip" network queries), while the alias resolves to
	// the same address in plain IPv4 form.
	r.answers["example.com"] = []net.IP{net.ParseIP("::ffff:203.0.113.9")}
	r.answers["app.example.org"] = ips("203.0.113.9")

	v := New("example.com", nil)
	v.lookupIP = r.lookup

	if err := v.Verify(context.Background(), "app.example.org"); err != nil {
		t.Fatalf("expected nil error for equivalent IPv4/IPv4-in-IPv6 addresses, got %v", err)
	}
}

func TestVerify_EmptyDomainNoStaticIPs(t *testing.T) {
	r := newStubResolver()

	v := New("", nil)
	v.lookupIP = r.lookup

	err := v.Verify(context.Background(), "app.example.org")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "does not know its own address") {
		t.Fatalf("error should explain the gateway cannot verify aliases, got: %v", err)
	}
}

func TestVerify_DomainUnresolvable(t *testing.T) {
	r := newStubResolver()
	r.fail["example.com"] = errors.New("no such host")

	v := New("example.com", nil)
	v.lookupIP = r.lookup

	err := v.Verify(context.Background(), "app.example.org")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "does not know its own address") {
		t.Fatalf("error should explain the gateway cannot verify aliases, got: %v", err)
	}
}

func TestVerify_ContextCancelled(t *testing.T) {
	v := New("example.com", nil)
	v.lookupIP = func(ctx context.Context, host string) ([]net.IP, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := v.Verify(ctx, "app.example.org")
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

func TestNew_SkipsUnparseableStaticIPs(t *testing.T) {
	v := New("example.com", []string{"not-an-ip", "203.0.113.9", ""})
	if len(v.staticIPs) != 1 || v.staticIPs[0].String() != "203.0.113.9" {
		t.Fatalf("expected only the valid entry to survive, got %v", v.staticIPs)
	}
}

func TestNewFromEnv_ReadsDomainAndPublicIPs(t *testing.T) {
	t.Setenv("DOMAIN", "example.com")
	t.Setenv("GATEWAY_PUBLIC_IPS", "203.0.113.9, 198.51.100.4")

	v := NewFromEnv()
	if v.domain != "example.com" {
		t.Fatalf("expected domain %q, got %q", "example.com", v.domain)
	}
	if len(v.staticIPs) != 2 {
		t.Fatalf("expected 2 static IPs, got %v", v.staticIPs)
	}
}

// TestVerify_GatewayCacheExpires isn't a strict requirement of the table
// above, but documents that the TTL is actually enforced rather than being a
// permanent cache once populated.
func TestVerify_GatewayCacheExpires(t *testing.T) {
	r := newStubResolver()
	r.answers["example.com"] = ips("203.0.113.9")
	r.answers["app.example.org"] = ips("203.0.113.9")

	v := New("example.com", nil)
	v.lookupIP = r.lookup
	v.cachedAt = time.Now().Add(-gatewayIPTTL - time.Second)
	v.cachedIPs = nil

	if err := v.Verify(context.Background(), "app.example.org"); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if hits := r.hitsFor("example.com"); hits != 1 {
		t.Fatalf("expected the stale cache to trigger exactly one re-lookup, got %d", hits)
	}
}

// An answer the resolver returned but we could not read says nothing about
// where the hostname points, so it must not be reported as a definite move —
// the periodic re-check acts on ErrPointsElsewhere and would otherwise take a
// working domain out of service over an answer it simply failed to parse.
func TestVerify_UnparseableAnswerIsNotADefiniteMove(t *testing.T) {
	v := New("gateway.example", []string{"198.51.100.4"})
	v.lookupIP = func(_ context.Context, host string) ([]net.IP, error) {
		// A slice that is neither 4 nor 16 bytes: netip.AddrFromSlice rejects it.
		return []net.IP{{1, 2, 3}}, nil
	}

	err := v.Verify(context.Background(), "app.example.org")
	if err == nil {
		t.Fatal("want an error for an unreadable answer")
	}
	if errors.Is(err, ErrPointsElsewhere) {
		t.Errorf("an unreadable answer was reported as a definite move: %v", err)
	}
}

// The mismatch that a re-check may act on must keep carrying the sentinel.
func TestVerify_MismatchCarriesErrPointsElsewhere(t *testing.T) {
	v := New("gateway.example", []string{"198.51.100.4"})
	v.lookupIP = func(_ context.Context, host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("203.0.113.9")}, nil
	}

	err := v.Verify(context.Background(), "app.example.org")
	if !errors.Is(err, ErrPointsElsewhere) {
		t.Fatalf("a real mismatch must carry ErrPointsElsewhere, got %v", err)
	}
}
