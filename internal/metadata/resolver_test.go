package metadata

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
)

// fakeLister is the container runtime's live view, and a counter for how often
// the resolver asks for it.
type fakeLister struct {
	instances []*runtime.Container
	err       error
	calls     int
}

func (f *fakeLister) List(context.Context, string) ([]*runtime.Container, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.instances, nil
}

func openDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func seedVM(t *testing.T, database *db.DB, id, name, incusName, ip string) *db.Container {
	t.Helper()
	if _, err := database.EnsureUser("owner", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	c := &db.Container{
		ID: id, Name: name, OwnerID: "owner", IncusName: incusName,
		Status: "running", IPAddress: ip, CPULimit: 2, MemoryMB: 2048, DiskGB: 30,
		AppPort: 3000, Nesting: true,
	}
	if err := database.CreateContainer(c); err != nil {
		t.Fatal(err)
	}
	return c
}

func addr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// The whole reason identity is resolved runtime-first. The containers table
// still records the address against the VM that used to hold it; the runtime
// knows it has moved. Trusting the table would hand the new holder somebody
// else's identity.
func TestAStaleAddressRowDoesNotDecideIdentity(t *testing.T) {
	database := openDB(t)
	seedVM(t, database, "c-a", "alpha", "svkexe-owner-alpha", "10.100.0.5")
	seedVM(t, database, "c-b", "beta", "svkexe-owner-beta", "")

	lister := &fakeLister{instances: []*runtime.Container{
		{ID: "svkexe-owner-beta", IP: "10.100.0.5", MAC: "00:16:3e:00:00:02", AddressFiltered: true},
	}}
	r := NewRuntimeResolver(lister, database, time.Minute, nil)

	id, err := r.Resolve(t.Context(), addr(t, "10.100.0.5"))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if id.ContainerID != "c-b" {
		t.Errorf("want the live holder c-b, got %s (%s)", id.ContainerID, id.Name)
	}
	if id.MAC != "00:16:3e:00:00:02" {
		t.Errorf("mac: want the live one, got %q", id.MAC)
	}
}

func TestResolutionIsCachedForOneWindow(t *testing.T) {
	database := openDB(t)
	seedVM(t, database, "c-a", "alpha", "svkexe-owner-alpha", "10.100.0.5")
	lister := &fakeLister{instances: []*runtime.Container{{ID: "svkexe-owner-alpha", IP: "10.100.0.5", AddressFiltered: true}}}

	c := &clock{t: time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)}
	r := NewRuntimeResolver(lister, database, 5*time.Second, c.now)

	for range 4 {
		if _, err := r.Resolve(t.Context(), addr(t, "10.100.0.5")); err != nil {
			t.Fatalf("resolve: %v", err)
		}
	}
	if lister.calls != 1 {
		t.Errorf("inside the window: want 1 runtime call, got %d", lister.calls)
	}

	c.add(5 * time.Second)
	if _, err := r.Resolve(t.Context(), addr(t, "10.100.0.5")); err != nil {
		t.Fatalf("resolve after the window: %v", err)
	}
	if lister.calls != 2 {
		t.Errorf("after the window: want 2 runtime calls, got %d", lister.calls)
	}
}

// An address that belongs to nothing must not turn into a runtime call per
// request: that is how a loop inside one VM becomes a denial of service against
// Incus for every VM.
func TestUnknownAddressRefreshesAtMostOncePerWindow(t *testing.T) {
	database := openDB(t)
	seedVM(t, database, "c-a", "alpha", "svkexe-owner-alpha", "10.100.0.5")
	lister := &fakeLister{instances: []*runtime.Container{{ID: "svkexe-owner-alpha", IP: "10.100.0.5", AddressFiltered: true}}}

	c := &clock{t: time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)}
	r := NewRuntimeResolver(lister, database, time.Minute, c.now)

	for range 50 {
		if _, err := r.Resolve(t.Context(), addr(t, "203.0.113.7")); err != ErrUnknownCaller {
			t.Fatalf("want ErrUnknownCaller, got %v", err)
		}
	}
	if lister.calls != 1 {
		t.Errorf("want 1 runtime call for 50 unattributable requests, got %d", lister.calls)
	}
}

func TestAddressFormsResolveToTheSameVM(t *testing.T) {
	database := openDB(t)
	seedVM(t, database, "c-a", "alpha", "svkexe-owner-alpha", "10.100.0.5")
	lister := &fakeLister{instances: []*runtime.Container{{ID: "svkexe-owner-alpha", IP: "10.100.0.5", AddressFiltered: true}}}
	r := NewRuntimeResolver(lister, database, time.Minute, nil)

	for _, form := range []string{"10.100.0.5", "::ffff:10.100.0.5"} {
		id, err := r.Resolve(t.Context(), addr(t, form))
		if err != nil {
			t.Fatalf("%s: %v", form, err)
		}
		if id.ContainerID != "c-a" {
			t.Errorf("%s resolved to %s", form, id.ContainerID)
		}
	}
}

// An instance the platform does not track — an image build container, say — is
// not one of its VMs, however reachable it is.
func TestAnInstanceWithoutARowIsNotAVM(t *testing.T) {
	database := openDB(t)
	lister := &fakeLister{instances: []*runtime.Container{{ID: "svkexe-base-build", IP: "10.100.0.9", AddressFiltered: true}}}
	r := NewRuntimeResolver(lister, database, time.Minute, nil)

	if _, err := r.Resolve(t.Context(), addr(t, "10.100.0.9")); err != ErrUnknownCaller {
		t.Errorf("want ErrUnknownCaller, got %v", err)
	}
}

// Two instances claiming one address identify neither. Awarding it to whichever
// the runtime happened to list first would be a coin toss over whose metadata
// gets served.
func TestAnAmbiguousAddressResolvesToNobody(t *testing.T) {
	database := openDB(t)
	seedVM(t, database, "c-a", "alpha", "svkexe-owner-alpha", "10.100.0.5")
	seedVM(t, database, "c-b", "beta", "svkexe-owner-beta", "10.100.0.5")
	lister := &fakeLister{instances: []*runtime.Container{
		{ID: "svkexe-owner-alpha", IP: "10.100.0.5", AddressFiltered: true},
		{ID: "svkexe-owner-beta", IP: "10.100.0.5", AddressFiltered: true},
	}}
	r := NewRuntimeResolver(lister, database, time.Minute, nil)

	if _, err := r.Resolve(t.Context(), addr(t, "10.100.0.5")); err != ErrUnknownCaller {
		t.Errorf("want ErrUnknownCaller, got %v", err)
	}
}

func TestARuntimeOutageServesTheLastKnownViewThenBacksOff(t *testing.T) {
	database := openDB(t)
	seedVM(t, database, "c-a", "alpha", "svkexe-owner-alpha", "10.100.0.5")
	lister := &fakeLister{instances: []*runtime.Container{{ID: "svkexe-owner-alpha", IP: "10.100.0.5", AddressFiltered: true}}}

	c := &clock{t: time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)}
	r := NewRuntimeResolver(lister, database, 5*time.Second, c.now)
	if _, err := r.Resolve(t.Context(), addr(t, "10.100.0.5")); err != nil {
		t.Fatal(err)
	}

	lister.err = context.DeadlineExceeded
	c.add(5 * time.Second)
	if _, err := r.Resolve(t.Context(), addr(t, "10.100.0.5")); err != nil {
		t.Errorf("a VM should keep its identity through a runtime outage: %v", err)
	}
	if lister.calls != 2 {
		t.Fatalf("want 2 runtime calls, got %d", lister.calls)
	}
	// The failed attempt still opened a window, so the outage does not turn into
	// a retry storm.
	for range 10 {
		if _, err := r.Resolve(t.Context(), addr(t, "10.100.0.5")); err != nil {
			t.Fatal(err)
		}
	}
	if lister.calls != 2 {
		t.Errorf("want no extra calls inside the window, got %d", lister.calls)
	}
}

// With no view at all there is nothing to fall back on, and a caller must be
// told the service cannot answer rather than that it is not a VM.
func TestARuntimeOutageBeforeAnyViewIsAnError(t *testing.T) {
	database := openDB(t)
	lister := &fakeLister{err: context.DeadlineExceeded}
	r := NewRuntimeResolver(lister, database, time.Minute, nil)

	_, err := r.Resolve(t.Context(), addr(t, "10.100.0.5"))
	if err == nil || err == ErrUnknownCaller {
		t.Errorf("want a plain error, got %v", err)
	}
}

// The whole tree is served unauthenticated to every process in the VM, so it has
// to be provably free of the platform's secrets.
func TestNoSecretReachesTheTree(t *testing.T) {
	database := openDB(t)
	c := seedVM(t, database, "c-a", "alpha", "svkexe-owner-alpha", "10.100.0.5")

	const llmKey = "sk-secret-llm-key-value"
	encKey := make([]byte, 32)
	for i := range encKey {
		encKey[i] = byte(i)
	}
	if err := database.CreateAPIKey("k-1", c.OwnerID, "openrouter", llmKey, encKey); err != nil {
		t.Fatal(err)
	}
	session, err := database.CreateSession(c.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetUserPassword(c.OwnerID, "$2a$12$secretbcrypthashvalue"); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateSSHKey(&db.SSHKey{
		ID: "s-1", UserID: c.OwnerID, Fingerprint: "SHA256:abc",
		PublicKey: "ssh-ed25519 AAAAC3Nz laptop", Name: "laptop",
	}); err != nil {
		t.Fatal(err)
	}

	lister := &fakeLister{instances: []*runtime.Container{
		{ID: "svkexe-owner-alpha", IP: "10.100.0.5", MAC: "00:16:3e:00:00:01", AddressFiltered: true},
	}}
	srv := New(NewRuntimeResolver(lister, database, time.Minute, nil), Config{
		Domain: "svk.exe", ImageID: "svkexe-base",
	})

	leaves := map[string]string{}
	walk(t, srv, "/latest/meta-data/", 0, leaves)
	leaves["/latest/dynamic/instance-identity/document"] = body(t, srv, "/latest/dynamic/instance-identity/document")

	secrets := map[string]string{
		"the LLM key":       llmKey,
		"a session token":   session.Token,
		"the password hash": "$2a$12$secretbcrypthashvalue",
	}
	for path, value := range leaves {
		for what, secret := range secrets {
			if strings.Contains(value, secret) {
				t.Errorf("%s publishes %s", path, what)
			}
		}
	}
	// The owner's own public key is not a secret and must be there, otherwise
	// this test could pass by publishing nothing at all.
	if got := leaves["/latest/meta-data/public-keys/0/openssh-key"]; got != "ssh-ed25519 AAAAC3Nz laptop" {
		t.Errorf("the owner's SSH key is missing from the tree: %q", got)
	}
	if got := leaves["/latest/meta-data/instance-id"]; got != "c-a" {
		t.Errorf("instance-id: got %q", got)
	}
}

// The VM is told the nesting that is actually in effect for it: the owner's wish
// capped by the operator's ceiling, which is the same resolution a start applies.
func TestNestingIsReportedAsResolvedAgainstTheCeiling(t *testing.T) {
	database := openDB(t)
	seedVM(t, database, "c-a", "alpha", "svkexe-owner-alpha", "10.100.0.5")
	lister := &fakeLister{instances: []*runtime.Container{{ID: "svkexe-owner-alpha", IP: "10.100.0.5", AddressFiltered: true}}}
	srv := New(NewRuntimeResolver(lister, database, 0, nil), Config{})

	if got := body(t, srv, "/latest/meta-data/svkexe/nesting"); got != "true" {
		t.Errorf("with nesting allowed: want true, got %q", got)
	}

	if err := database.SetNestingAllowed(false); err != nil {
		t.Fatal(err)
	}
	// A fresh service so the resolver's window does not hide the change.
	srv = New(NewRuntimeResolver(lister, database, 0, nil), Config{})
	if got := body(t, srv, "/latest/meta-data/svkexe/nesting"); got != "false" {
		t.Errorf("with the ceiling down: want false, got %q", got)
	}
}

// The service is a plain handler over a resolver, so the resolver is the only
// thing that decides who the caller is — worth pinning, because a future
// middleware that trusted a header would break exactly this.
func TestIdentityComesFromTheSocketAlone(t *testing.T) {
	database := openDB(t)
	seedVM(t, database, "c-a", "alpha", "svkexe-owner-alpha", "10.100.0.5")
	seedVM(t, database, "c-b", "beta", "svkexe-owner-beta", "10.100.0.6")
	lister := &fakeLister{instances: []*runtime.Container{
		{ID: "svkexe-owner-alpha", IP: "10.100.0.5", AddressFiltered: true},
		{ID: "svkexe-owner-beta", IP: "10.100.0.6", AddressFiltered: true},
	}}
	srv := New(NewRuntimeResolver(lister, database, time.Minute, nil), Config{})

	w := do(t, srv, http.MethodGet, "/latest/meta-data/instance-id", map[string]string{
		"Host":             "10.100.0.6",
		"X-Real-Ip":        "10.100.0.6",
		"X-Forwarded-Host": "beta",
	})
	if w.Code != http.StatusOK || w.Body.String() != "c-a" {
		t.Errorf("headers must not change the answer: status %d body %q", w.Code, w.Body.String())
	}
}

// The service's entire notion of identity is the source address, and on a plain
// Linux bridge a tenant with root in their own VM can claim a neighbour's. There
// is nothing in the request that would give that away, so a VM the runtime is not
// pinning to its own address is not answered at all.
func TestAnUnpinnedInstanceIsRefused(t *testing.T) {
	database := openDB(t)
	seedVM(t, database, "c-a", "alpha", "svkexe-owner-alpha", "10.100.0.5")
	lister := &fakeLister{instances: []*runtime.Container{
		{ID: "svkexe-owner-alpha", IP: "10.100.0.5"}, // no security.ipv4_filtering
	}}
	r := NewRuntimeResolver(lister, database, time.Minute, nil)

	if _, err := r.Resolve(t.Context(), addr(t, "10.100.0.5")); err != ErrUnknownCaller {
		t.Fatalf("an unfiltered NIC must not be served: got %v", err)
	}

	// And the same VM once the runtime is pinning it — so the refusal is about
	// the filtering and not about anything else in the fixture.
	lister.instances[0].AddressFiltered = true
	r = NewRuntimeResolver(lister, database, time.Minute, nil)
	id, err := r.Resolve(t.Context(), addr(t, "10.100.0.5"))
	if err != nil {
		t.Fatalf("a pinned VM should be served: %v", err)
	}
	if id.ContainerID != "c-a" {
		t.Errorf("got %s", id.ContainerID)
	}
}

// Every answer costs database reads on the pool the dashboard, the API and the
// SSH gateway share. A VM polling its own metadata must not be able to spend
// that on everyone's behalf.
func TestTheDatabaseIsReadOncePerWindowNotPerRequest(t *testing.T) {
	database := openDB(t)
	seedVM(t, database, "c-a", "alpha", "svkexe-owner-alpha", "10.100.0.5")
	lister := &fakeLister{instances: []*runtime.Container{
		{ID: "svkexe-owner-alpha", IP: "10.100.0.5", AddressFiltered: true},
	}}
	c := &clock{t: time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)}
	r := NewRuntimeResolver(lister, database, 5*time.Second, c.now)

	if _, err := r.Resolve(t.Context(), addr(t, "10.100.0.5")); err != nil {
		t.Fatal(err)
	}
	// Deleting the row proves the second answer came from the cache rather than
	// from another query.
	if err := database.DeleteContainer("c-a"); err != nil {
		t.Fatal(err)
	}
	id, err := r.Resolve(t.Context(), addr(t, "10.100.0.5"))
	if err != nil {
		t.Fatalf("a second request inside the window should be served from cache: %v", err)
	}
	if id.ContainerID != "c-a" {
		t.Errorf("got %s", id.ContainerID)
	}

	// The next window has to see the world as it now is.
	c.add(5 * time.Second)
	if _, err := r.Resolve(t.Context(), addr(t, "10.100.0.5")); err != ErrUnknownCaller {
		t.Errorf("after the window a deleted VM must stop being served: %v", err)
	}
}

// A cached identity still has to answer for the address the request came from,
// not for the one that first filled the cache.
func TestACachedIdentityStillReportsTheCallersOwnAddress(t *testing.T) {
	database := openDB(t)
	seedVM(t, database, "c-a", "alpha", "svkexe-owner-alpha", "10.100.0.5")
	// The slice-valued fields have to be populated, or the aliasing this is
	// about cannot show up.
	alias, err := database.CreateContainerAlias("c-a", "demo.example.org")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetAliasVerification(alias.ID, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateSSHKey(&db.SSHKey{
		ID: "s-1", UserID: "owner", Fingerprint: "SHA256:abc",
		PublicKey: "ssh-ed25519 AAAAC3Nz laptop", Name: "laptop",
	}); err != nil {
		t.Fatal(err)
	}
	// One instance, two addresses — an unusual but legal configuration, and the
	// cheapest way to show the per-caller fields are not cached.
	lister := &fakeLister{instances: []*runtime.Container{
		{ID: "svkexe-owner-alpha", IP: "10.100.0.5", MAC: "00:16:3e:00:00:01", AddressFiltered: true},
	}}
	r := NewRuntimeResolver(lister, database, time.Minute, nil)

	first, err := r.Resolve(t.Context(), addr(t, "10.100.0.5"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Resolve(t.Context(), addr(t, "::ffff:10.100.0.5"))
	if err != nil {
		t.Fatal(err)
	}
	if first.IPv4 != "10.100.0.5" || second.IPv4 != "10.100.0.5" {
		t.Errorf("addresses: %q and %q", first.IPv4, second.IPv4)
	}
	// Mutating one must not reach the other, or one VM's request could edit what
	// the next one is told. The slices matter more than the scalars here: a
	// shallow copy shares their backing arrays, so an in-place edit or an append
	// within capacity would be read by every later caller in the window.
	first.Name = "tampered"
	if len(first.Aliases) > 0 {
		first.Aliases[0] = "tampered.example.org"
	}
	if len(first.PublicKeys) > 0 {
		first.PublicKeys[0].Key = "ssh-ed25519 TAMPERED"
	}
	third, err := r.Resolve(t.Context(), addr(t, "10.100.0.5"))
	if err != nil {
		t.Fatal(err)
	}
	if third.Name != "alpha" {
		t.Errorf("the cached identity was mutated through a returned copy: %q", third.Name)
	}
	if len(third.Aliases) == 0 || third.Aliases[0] != "demo.example.org" {
		t.Errorf("the cached aliases share a backing array with a returned copy: %v", third.Aliases)
	}
	if len(third.PublicKeys) == 0 || third.PublicKeys[0].Key != "ssh-ed25519 AAAAC3Nz laptop" {
		t.Errorf("the cached keys share a backing array with a returned copy: %v", third.PublicKeys)
	}
}

// Serving the last known view through a brief hiccup is right; serving it for as
// long as the runtime stays down is not, because the longer it is stale the
// likelier an address has changed hands and the answer belongs to someone else.
func TestAStaleViewIsEventuallyRefusedRatherThanTrusted(t *testing.T) {
	database := openDB(t)
	seedVM(t, database, "c-a", "alpha", "svkexe-owner-alpha", "10.100.0.5")
	lister := &fakeLister{instances: []*runtime.Container{
		{ID: "svkexe-owner-alpha", IP: "10.100.0.5", AddressFiltered: true},
	}}
	c := &clock{t: time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)}
	r := NewRuntimeResolver(lister, database, 5*time.Second, c.now)
	if _, err := r.Resolve(t.Context(), addr(t, "10.100.0.5")); err != nil {
		t.Fatal(err)
	}

	lister.err = context.DeadlineExceeded
	for window := 1; window <= maxStaleWindows; window++ {
		c.add(5 * time.Second)
		if _, err := r.Resolve(t.Context(), addr(t, "10.100.0.5")); err != nil {
			t.Fatalf("window %d should still be served from the last view: %v", window, err)
		}
	}
	c.add(5 * time.Second)
	_, err := r.Resolve(t.Context(), addr(t, "10.100.0.5"))
	if !errors.Is(err, ErrStaleView) {
		t.Fatalf("past the ceiling the caller must be told the service cannot answer, got %v", err)
	}
	// And for the REST of that window, not only for the request that happened to
	// cross the ceiling. A failed refresh moves the window on, so a ceiling
	// enforced only where refreshes happen refuses one request and serves the
	// stale view to every other request for the next five seconds.
	for i := range 3 {
		c.add(time.Second)
		if _, err := r.Resolve(t.Context(), addr(t, "10.100.0.5")); !errors.Is(err, ErrStaleView) {
			t.Fatalf("%ds after the ceiling, inside the same window: want ErrStaleView, got %v", i+1, err)
		}
	}
	// Not a 403: the caller IS a VM, the platform just cannot prove which one.
	if errors.Is(err, ErrUnknownCaller) {
		t.Error("a stale view must not be reported as an unknown caller")
	}

	// And recovery restores service without a restart.
	lister.err = nil
	c.add(5 * time.Second)
	if _, err := r.Resolve(t.Context(), addr(t, "10.100.0.5")); err != nil {
		t.Errorf("after the runtime came back: %v", err)
	}
}
