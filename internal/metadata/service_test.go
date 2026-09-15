package metadata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// fakeResolver stands in for the runtime-backed one where the test is about the
// HTTP surface rather than about how a caller is identified.
type fakeResolver struct {
	byAddr map[string]*Identity
	err    error
}

func (f *fakeResolver) Resolve(_ context.Context, addr netip.Addr) (*Identity, error) {
	if f.err != nil {
		return nil, f.err
	}
	if id, ok := f.byAddr[addr.String()]; ok {
		return id, nil
	}
	return nil, ErrUnknownCaller
}

const callerIP = "10.100.0.5"

func sampleIdentity() *Identity {
	return &Identity{
		ContainerID:      "c-1",
		Name:             "box",
		IncusName:        "svkexe-owner-box",
		OwnerID:          "owner",
		IPv4:             callerIP,
		MAC:              "00:16:3e:aa:bb:cc",
		CPULimit:         2,
		MemoryMB:         2048,
		DiskGB:           30,
		AppPort:          3000,
		AppPublic:        true,
		Nesting:          true,
		NestingApplied:   true,
		InitialTask:      "build the thing",
		InitialTaskState: "done",
		Aliases:          []string{"demo.example.org"},
		PublicKeys:       []PublicKey{{Name: "laptop", Key: "ssh-ed25519 AAAAC3Nz laptop"}},
		CreatedAt:        time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC),
	}
}

func testService(t *testing.T, id *Identity) *Service {
	t.Helper()
	return New(&fakeResolver{byAddr: map[string]*Identity{callerIP: id}}, Config{
		Domain:    "svk.exe",
		PublicIPs: []string{"203.0.113.9"},
		ImageID:   "svkexe-base",
	})
}

// get issues a request from the VM's own address, which is the service's entire
// notion of who is asking.
func get(t *testing.T, srv http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, srv, http.MethodGet, path, nil)
}

func do(t *testing.T, srv http.Handler, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = callerIP + ":41234"
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

func body(t *testing.T, srv http.Handler, path string) string {
	t.Helper()
	w := get(t, srv, path)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d, body %q", path, w.Code, w.Body.String())
	}
	return w.Body.String()
}

func TestVersionIndexAndLatestListing(t *testing.T) {
	srv := testService(t, sampleIdentity())

	if got := body(t, srv, "/"); got != "latest\n" {
		t.Errorf("GET /: want %q, got %q", "latest\n", got)
	}
	// user-data is absent from the listing because the platform publishes none,
	// and api/ is unadvertised exactly as on EC2.
	if got := body(t, srv, "/latest/"); got != "dynamic/\nmeta-data/\n" {
		t.Errorf("GET /latest/: want %q, got %q", "dynamic/\nmeta-data/\n", got)
	}
	if ct := get(t, srv, "/latest/").Header().Get("Content-Type"); ct != "text/plain" {
		t.Errorf("content type: want text/plain, got %q", ct)
	}
}

func TestMetaDataListingNamesTheExpectedKeys(t *testing.T) {
	srv := testService(t, sampleIdentity())
	listing := body(t, srv, "/latest/meta-data/")

	entries := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(listing, "\n"), "\n") {
		entries[line] = true
	}
	for _, want := range []string{
		"ami-id", "ami-launch-index", "hostname", "instance-action", "instance-id",
		"instance-life-cycle", "instance-type", "local-hostname", "local-ipv4", "mac",
		"network/", "placement/", "public-hostname", "public-ipv4", "public-keys/",
		"reservation-id", "security-groups", "services/", "svkexe/",
	} {
		if !entries[want] {
			t.Errorf("listing is missing %q; got:\n%s", want, listing)
		}
	}
	// A directory must never be advertised as a leaf, nor the other way round.
	for _, dirEntry := range []string{"placement", "public-keys", "services", "svkexe", "network"} {
		if entries[dirEntry] {
			t.Errorf("directory %q listed without its trailing slash", dirEntry)
		}
	}
	if entries["instance-id/"] {
		t.Error("leaf instance-id listed as a directory")
	}
}

// Every entry a listing advertises has to answer, because walking the tree is
// what discovery tooling does. A key that is listed and not implemented — or
// implemented and not listed — is the failure mode this guards.
func TestWholeTreeIsWalkable(t *testing.T) {
	srv := testService(t, sampleIdentity())
	leaves := map[string]string{}
	walk(t, srv, "/latest/meta-data/", 0, leaves)

	for _, want := range []string{
		"/latest/meta-data/instance-id",
		"/latest/meta-data/placement/region",
		"/latest/meta-data/public-keys/0/openssh-key",
		"/latest/meta-data/network/interfaces/macs/00:16:3e:aa:bb:cc/local-ipv4s",
		"/latest/meta-data/svkexe/app-port",
	} {
		if _, ok := leaves[want]; !ok {
			t.Errorf("the walk never reached %s", want)
		}
	}
}

// walk fetches a directory and every entry it names, recursively, asserting the
// two invariants the tree promises: a directory answers with a listing and a
// leaf answers with a value that carries no trailing newline.
func walk(t *testing.T, srv http.Handler, dirPath string, depth int, leaves map[string]string) {
	t.Helper()
	if depth > 8 {
		t.Fatalf("tree deeper than expected at %s", dirPath)
	}
	listing := body(t, srv, dirPath)
	if listing != "" && !strings.HasSuffix(listing, "\n") {
		t.Errorf("listing %s is not newline-terminated: %q", dirPath, listing)
	}
	for _, entry := range strings.Split(strings.TrimSuffix(listing, "\n"), "\n") {
		if entry == "" {
			continue
		}
		child := dirPath + entry
		if strings.HasSuffix(entry, "/") {
			walk(t, srv, child, depth+1, leaves)
			continue
		}
		// EC2 lists an SSH key slot as "0=my-key" with no trailing slash even
		// though it is a directory, and every IMDS client knows to read the
		// index before the "=" as the path segment.
		if index, _, labelled := strings.Cut(entry, "="); labelled {
			walk(t, srv, dirPath+index+"/", depth+1, leaves)
			continue
		}
		value := body(t, srv, child)
		if strings.HasSuffix(value, "\n") {
			t.Errorf("leaf %s ends with a newline: %q", child, value)
		}
		leaves[child] = value
	}
}

func TestLeafValues(t *testing.T) {
	srv := testService(t, sampleIdentity())

	for _, tc := range []struct{ path, want string }{
		{"/latest/meta-data/instance-id", "c-1"},
		{"/latest/meta-data/local-ipv4", callerIP},
		{"/latest/meta-data/hostname", "svkexe-owner-box"},
		{"/latest/meta-data/local-hostname", "svkexe-owner-box"},
		{"/latest/meta-data/public-hostname", "box.svk.exe"},
		{"/latest/meta-data/public-ipv4", "203.0.113.9"},
		{"/latest/meta-data/ami-id", "svkexe-base"},
		{"/latest/meta-data/instance-type", "svkexe.c2-m2048"},
		{"/latest/meta-data/instance-life-cycle", "on-demand"},
		{"/latest/meta-data/mac", "00:16:3e:aa:bb:cc"},
		{"/latest/meta-data/placement/region", "svkexe"},
		{"/latest/meta-data/placement/availability-zone", "svkexe-a"},
		{"/latest/meta-data/services/domain", "svk.exe"},
		{"/latest/meta-data/public-keys/0=laptop/openssh-key", "ssh-ed25519 AAAAC3Nz laptop"},
		{"/latest/meta-data/svkexe/app-port", "3000"},
		{"/latest/meta-data/svkexe/app-public", "true"},
		{"/latest/meta-data/svkexe/owner-id", "owner"},
		{"/latest/meta-data/svkexe/agent-host", "agent-box.svk.exe"},
		{"/latest/meta-data/svkexe/aliases", "demo.example.org"},
		{"/latest/meta-data/svkexe/nesting", "true"},
		{"/latest/meta-data/svkexe/initial-task", "build the thing"},
	} {
		if got := body(t, srv, tc.path); got != tc.want {
			t.Errorf("GET %s: want %q, got %q", tc.path, tc.want, got)
		}
	}

	if got := body(t, srv, "/latest/meta-data/public-keys/"); got != "0=laptop\n" {
		t.Errorf("public-keys listing: want %q, got %q", "0=laptop\n", got)
	}
}

func TestIdentityDocument(t *testing.T) {
	srv := testService(t, sampleIdentity())
	raw := body(t, srv, "/latest/dynamic/instance-identity/document")

	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("identity document is not JSON: %v (%q)", err, raw)
	}
	for key, want := range map[string]string{
		"instanceId":       "c-1",
		"imageId":          "svkexe-base",
		"privateIp":        callerIP,
		"region":           "svkexe",
		"availabilityZone": "svkexe-a",
		"pendingTime":      "2026-09-11T03:00:00Z",
	} {
		if got, _ := doc[key].(string); got != want {
			t.Errorf("document %s: want %q, got %q", key, want, got)
		}
	}
}

// The platform publishes no user-data. Answering with something anyway would
// hand a guest cloud-init a script it never asked for.
func TestUserDataIsNotPublished(t *testing.T) {
	srv := testService(t, sampleIdentity())
	if w := get(t, srv, "/latest/user-data"); w.Code != http.StatusNotFound {
		t.Errorf("GET /latest/user-data: want 404, got %d", w.Code)
	}
}

func TestUnknownPathsAreNotFound(t *testing.T) {
	srv := testService(t, sampleIdentity())
	for _, path := range []string{
		"/latest/meta-data/iam/security-credentials/",
		"/latest/meta-data/nope",
		"/latest/meta-data/local-ipv4/", // a leaf is not a directory
		"/2016-09-02/meta-data/",
		"/latest/dynamic/instance-identity/signature",
	} {
		if w := get(t, srv, path); w.Code != http.StatusNotFound {
			t.Errorf("GET %s: want 404, got %d", path, w.Code)
		}
	}
}

// A caller the platform cannot attribute to a VM learns nothing at all — not
// even that some other VM exists.
func TestUnknownCallerIsForbidden(t *testing.T) {
	srv := testService(t, sampleIdentity())
	r := httptest.NewRequest(http.MethodGet, "/latest/meta-data/instance-id", nil)
	r.RemoteAddr = "203.0.113.7:9999"
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}
	for _, secret := range []string{"c-1", "box", "svkexe-owner-box", "owner"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Errorf("the refusal names %q: %q", secret, w.Body.String())
		}
	}
}

// A dual-stack listener reports an IPv4 caller in mapped form, and a RemoteAddr
// always carries a port. Both are the same VM.
func TestCallerAddressForms(t *testing.T) {
	srv := testService(t, sampleIdentity())
	for _, remote := range []string{callerIP + ":41234", "[::ffff:10.100.0.5]:41234"} {
		r := httptest.NewRequest(http.MethodGet, "/latest/meta-data/instance-id", nil)
		r.RemoteAddr = remote
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Body.String() != "c-1" {
			t.Errorf("RemoteAddr %s: status %d body %q", remote, w.Code, w.Body.String())
		}
	}
}

// An identity with nothing to say about a key must not advertise it: the
// listing is the service's promise that every entry answers.
func TestSparseIdentityOmitsKeysRatherThanEmptyingThem(t *testing.T) {
	sparse := &Identity{ContainerID: "c-2", Name: "bare", IncusName: "svkexe-owner-bare", OwnerID: "owner", IPv4: callerIP}
	srv := New(&fakeResolver{byAddr: map[string]*Identity{callerIP: sparse}}, Config{})

	listing := body(t, srv, "/latest/meta-data/")
	for _, absent := range []string{"mac", "network/", "public-hostname", "public-ipv4", "public-keys/", "ami-id"} {
		if strings.Contains(listing, absent+"\n") {
			t.Errorf("listing advertises %q with nothing behind it:\n%s", absent, listing)
		}
	}
	if got := body(t, srv, "/latest/meta-data/instance-type"); got != "svkexe.custom" {
		t.Errorf("instance-type without limits: want svkexe.custom, got %q", got)
	}
	if got := body(t, srv, "/latest/meta-data/services/domain"); got != internalDomain {
		t.Errorf("services/domain without a configured domain: want %q, got %q", internalDomain, got)
	}
	// The walk must still hold for a VM with almost nothing to report.
	walk(t, srv, "/latest/meta-data/", 0, map[string]string{})
}

func TestResolverFailureIsNotAForbid(t *testing.T) {
	srv := New(&fakeResolver{err: context.DeadlineExceeded}, Config{})
	if w := get(t, srv, "/latest/meta-data/instance-id"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("want 503 when the runtime cannot be reached, got %d", w.Code)
	}
}
