package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dbpkg "github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/updater"
	"github.com/skrashevich/svkexe/internal/version"
)

// testLocalSHA is the commit the fake local build claims to be. It has to look
// like a real object ID so the checker compares it against the stub's sha
// instead of short-circuiting on IsDev.
const testLocalSHA = "1111111111111111111111111111111111111111"

// fakeBuild is the build metadata injected into the checker in place of the
// linker-stamped one, so the tests do not depend on how the test binary was
// built.
func fakeBuild(commit string) version.Info {
	return version.Info{
		Version:         "v9.9.9-test",
		Commit:          commit,
		BuildDate:       "2026-01-01T00:00:00Z",
		PicoClawVersion: "v0.0.0-test",
		ShelleyCommit:   "2222222222222222222222222222222222222222",
	}
}

// stubGitHub serves the single commits endpoint the branch channel calls,
// counting requests so cache behaviour is observable.
func stubGitHub(t *testing.T, sha string, status int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if status != http.StatusOK {
			http.Error(w, "boom", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"sha":%q,"html_url":"https://github.com/x/y/commit/%s","commit":{"committer":{"date":"2026-02-03T04:05:06Z"}}}`, sha, sha)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// newUpdaterService builds a Service pointed at a stub API and a writable
// state directory carrying the watcher marker, so Available() reports the
// deployment as able to update itself.
func newUpdaterService(t *testing.T, apiBase string, local version.Info, dir string) *updater.Service {
	t.Helper()
	writeWatcherMarker(t, dir)
	return updater.NewService(
		updater.Config{APIBase: apiBase, Local: &local, CacheTTL: time.Hour},
		updater.RunnerConfig{
			TriggerPath: filepath.Join(dir, "update.trigger"),
			StatusPath:  filepath.Join(dir, "update-status.json"),
			WatcherPath: filepath.Join(dir, "update-watcher"),
		},
	)
}

// writeWatcherMarker stands in for the marker scripts/install-update-units.sh
// writes once the root-owned svkexe-update.path unit is enabled. Without it the
// runner correctly refuses to write a trigger nothing would consume.
func writeWatcherMarker(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "update-watcher"), []byte("svkexe-update.path\n"), 0o644); err != nil {
		t.Fatalf("write watcher marker: %v", err)
	}
}

// newTestServerWithUpdater mirrors newTestServerWithAdmin but wires a real
// updater.Service into the Server, which the shared helpers deliberately leave
// nil.
func newTestServerWithUpdater(t *testing.T, upd *updater.Service) *Server {
	t.Helper()
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	_ = database.CreateUser(&dbpkg.User{ID: "user1", Email: "user1@example.com", Role: "user"})
	sess, err := database.CreateSession("user1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	testSessionToken = sess.Token

	_ = database.CreateUser(&dbpkg.User{ID: "admin1", Email: "admin@example.com", Role: "admin"})
	adminSess, err := database.CreateSession("admin1")
	if err != nil {
		t.Fatalf("create admin session: %v", err)
	}
	testAdminToken = adminSess.Token

	return NewServer(database, newMockRuntime(), testEncKey, "", nil, nil, nil, nil, upd)
}

// seedStatus writes an update status file the way scripts/update.sh would.
func seedStatus(t *testing.T, dir string, st updater.RunState) {
	t.Helper()
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("encode seeded status: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "update-status.json"), data, 0o600); err != nil {
		t.Fatalf("write seeded status: %v", err)
	}
}

// do runs a request against srv and returns the recorder.
func do(srv *Server, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

// updateEndpoints is every route the update feature adds, used by the
// authorization and disabled-deployment tables.
var updateEndpoints = []struct {
	name   string
	method string
	path   string
}{
	{"version", http.MethodGet, "/api/admin/version"},
	{"check", http.MethodGet, "/api/admin/update/check"},
	{"status", http.MethodGet, "/api/admin/update/status"},
	{"start", http.MethodPost, "/api/admin/update"},
}

func TestUpdateEndpoints_NonAdminForbidden(t *testing.T) {
	srv, _ := newTestServerWithAdmin(t)
	for _, tc := range updateEndpoints {
		t.Run(tc.name, func(t *testing.T) {
			// user1 has role "user"; the admin group must reject it before any
			// handler runs.
			w := do(srv, authedRequest(tc.method, tc.path, nil))
			if w.Code != http.StatusForbidden {
				t.Errorf("want 403, got %d: %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestAdminVersion(t *testing.T) {
	srv, _ := newTestServerWithAdmin(t)
	w := do(srv, authedAdminRequest(http.MethodGet, "/api/admin/version"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	var info version.Info
	if err := json.NewDecoder(w.Body).Decode(&info); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if info.Version == "" {
		t.Error("version must not be empty")
	}
	if info.Commit == "" {
		t.Error("commit must not be empty")
	}
}

func TestAdminVersion_UsesUpdaterBuildInfo(t *testing.T) {
	stub, _ := stubGitHub(t, testLocalSHA, http.StatusOK)
	srv := newTestServerWithUpdater(t, newUpdaterService(t, stub.URL, fakeBuild(testLocalSHA), t.TempDir()))

	w := do(srv, authedAdminRequest(http.MethodGet, "/api/admin/version"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var info version.Info
	if err := json.NewDecoder(w.Body).Decode(&info); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if info.Commit != testLocalSHA {
		t.Errorf("commit = %q, want %q", info.Commit, testLocalSHA)
	}
	if info.Version != "v9.9.9-test" {
		t.Errorf("version = %q, want v9.9.9-test", info.Version)
	}
}

func TestAdminUpdateCheck(t *testing.T) {
	const remoteSHA = "3333333333333333333333333333333333333333"

	tests := []struct {
		name      string
		remote    string
		wantAvail bool
	}{
		{"newer commit upstream", remoteSHA, true},
		{"already up to date", testLocalSHA, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub, _ := stubGitHub(t, tc.remote, http.StatusOK)
			srv := newTestServerWithUpdater(t, newUpdaterService(t, stub.URL, fakeBuild(testLocalSHA), t.TempDir()))

			w := do(srv, authedAdminRequest(http.MethodGet, "/api/admin/update/check"))
			if w.Code != http.StatusOK {
				t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
			}

			var st updater.Status
			if err := json.NewDecoder(w.Body).Decode(&st); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if st.UpdateAvailable != tc.wantAvail {
				t.Errorf("updateAvailable = %v, want %v (reason %q)", st.UpdateAvailable, tc.wantAvail, st.Reason)
			}
			if st.Latest == nil {
				t.Fatal("latest release missing from response")
			}
			if st.Latest.Commit != tc.remote {
				t.Errorf("latest commit = %q, want %q", st.Latest.Commit, tc.remote)
			}
			if st.Current.Commit != testLocalSHA {
				t.Errorf("current commit = %q, want %q", st.Current.Commit, testLocalSHA)
			}
		})
	}
}

func TestAdminUpdateCheck_ForceBypassesCache(t *testing.T) {
	stub, calls := stubGitHub(t, "3333333333333333333333333333333333333333", http.StatusOK)
	srv := newTestServerWithUpdater(t, newUpdaterService(t, stub.URL, fakeBuild(testLocalSHA), t.TempDir()))

	if w := do(srv, authedAdminRequest(http.MethodGet, "/api/admin/update/check")); w.Code != http.StatusOK {
		t.Fatalf("first check: want 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("after first check: %d upstream calls, want 1", got)
	}

	// A dashboard poll must not spend a GitHub rate-limit token per view.
	if w := do(srv, authedAdminRequest(http.MethodGet, "/api/admin/update/check")); w.Code != http.StatusOK {
		t.Fatalf("cached check: want 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("second check hit upstream: %d calls, want 1", got)
	}

	for _, force := range []string{"1", "true", "YES"} {
		before := calls.Load()
		w := do(srv, authedAdminRequest(http.MethodGet, "/api/admin/update/check?force="+force))
		if w.Code != http.StatusOK {
			t.Fatalf("force=%s: want 200, got %d: %s", force, w.Code, w.Body.String())
		}
		if got := calls.Load(); got != before+1 {
			t.Errorf("force=%s: %d upstream calls, want %d", force, got, before+1)
		}
	}
}

func TestAdminUpdateCheck_UpstreamFailureIsBadGateway(t *testing.T) {
	stub, _ := stubGitHub(t, "", http.StatusInternalServerError)
	srv := newTestServerWithUpdater(t, newUpdaterService(t, stub.URL, fakeBuild(testLocalSHA), t.TempDir()))

	w := do(srv, authedAdminRequest(http.MethodGet, "/api/admin/update/check"))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "update check") {
		t.Errorf("body should explain the failure, got %q", w.Body.String())
	}
}

func TestAdminUpdateStatus(t *testing.T) {
	dir := t.TempDir()
	stub, _ := stubGitHub(t, testLocalSHA, http.StatusOK)
	srv := newTestServerWithUpdater(t, newUpdaterService(t, stub.URL, fakeBuild(testLocalSHA), dir))

	started := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	seedStatus(t, dir, updater.RunState{
		State:     updater.StateRunning,
		StartedAt: started,
		Commit:    "3333333333333333333333333333333333333333",
		Log:       "building gateway",
	})

	w := do(srv, authedAdminRequest(http.MethodGet, "/api/admin/update/status"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	var st updater.RunState
	if err := json.NewDecoder(w.Body).Decode(&st); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if st.State != updater.StateRunning {
		t.Errorf("state = %q, want %q", st.State, updater.StateRunning)
	}
	if !st.StartedAt.Equal(started) {
		t.Errorf("startedAt = %s, want %s", st.StartedAt, started)
	}
	if st.Log != "building gateway" {
		t.Errorf("log = %q, want %q", st.Log, "building gateway")
	}
}

func TestAdminUpdateStatus_IdleWhenNeverRun(t *testing.T) {
	stub, _ := stubGitHub(t, testLocalSHA, http.StatusOK)
	srv := newTestServerWithUpdater(t, newUpdaterService(t, stub.URL, fakeBuild(testLocalSHA), t.TempDir()))

	w := do(srv, authedAdminRequest(http.MethodGet, "/api/admin/update/status"))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var st updater.RunState
	if err := json.NewDecoder(w.Body).Decode(&st); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if st.State != updater.StateIdle {
		t.Errorf("state = %q, want %q", st.State, updater.StateIdle)
	}
}

func TestAdminUpdateStart(t *testing.T) {
	dir := t.TempDir()
	stub, _ := stubGitHub(t, "3333333333333333333333333333333333333333", http.StatusOK)
	srv := newTestServerWithUpdater(t, newUpdaterService(t, stub.URL, fakeBuild(testLocalSHA), dir))

	w := do(srv, authedAdminRequest(http.MethodPost, "/api/admin/update"))
	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", w.Code, w.Body.String())
	}

	var st updater.RunState
	if err := json.NewDecoder(w.Body).Decode(&st); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if st.State != updater.StateRunning {
		t.Errorf("state = %q, want %q", st.State, updater.StateRunning)
	}

	// The trigger file is the whole mechanism: a root-owned path unit watches
	// for it. If it is not on disk, nothing will ever run.
	if _, err := os.Stat(filepath.Join(dir, "update.trigger")); err != nil {
		t.Fatalf("trigger file not written: %v", err)
	}
}

func TestAdminUpdateStart_ConflictWhileRunning(t *testing.T) {
	dir := t.TempDir()
	stub, _ := stubGitHub(t, "3333333333333333333333333333333333333333", http.StatusOK)
	srv := newTestServerWithUpdater(t, newUpdaterService(t, stub.URL, fakeBuild(testLocalSHA), dir))

	seedStatus(t, dir, updater.RunState{State: updater.StateRunning, StartedAt: time.Now().UTC()})

	w := do(srv, authedAdminRequest(http.MethodPost, "/api/admin/update"))
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "in progress") {
		t.Errorf("body should name the conflict, got %q", w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "update.trigger")); !os.IsNotExist(err) {
		t.Errorf("a conflicting request must not write a trigger (stat err %v)", err)
	}
}

func TestAdminUpdateStart_UnavailableDeployment(t *testing.T) {
	dir := t.TempDir()
	stub, _ := stubGitHub(t, "3333333333333333333333333333333333333333", http.StatusOK)
	// A writable data directory with no svkexe-update.path unit watching it —
	// a container image, or a host upgraded before the units existed. Writing a
	// trigger here would hang the UI on an update nothing can start, so the
	// endpoint must refuse.
	upd := updater.NewService(
		updater.Config{APIBase: stub.URL, Local: ptr(fakeBuild(testLocalSHA)), CacheTTL: time.Hour},
		updater.RunnerConfig{
			TriggerPath: filepath.Join(dir, "update.trigger"),
			StatusPath:  filepath.Join(dir, "update-status.json"),
			WatcherPath: filepath.Join(dir, "update-watcher"), // deliberately never written
		},
	)
	srv := newTestServerWithUpdater(t, upd)

	w := do(srv, authedAdminRequest(http.MethodPost, "/api/admin/update"))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no update watcher is installed") {
		t.Errorf("body should carry the runner's reason, got %q", w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "update.trigger")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused request must not write a trigger (stat err %v)", err)
	}
}

func TestUpdateEndpoints_NilUpdater(t *testing.T) {
	// newTestServerWithAdmin builds the Server with a nil updater, which is the
	// deployment that was never wired for self-update.
	srv, _ := newTestServerWithAdmin(t)
	for _, tc := range updateEndpoints {
		t.Run(tc.name, func(t *testing.T) {
			want := http.StatusServiceUnavailable
			if tc.name == "version" {
				want = http.StatusOK
			}
			w := do(srv, authedAdminRequest(tc.method, tc.path))
			if w.Code != want {
				t.Fatalf("want %d, got %d: %s", want, w.Code, w.Body.String())
			}
			if want == http.StatusServiceUnavailable && !strings.Contains(w.Body.String(), "not configured") {
				t.Errorf("body should explain why, got %q", w.Body.String())
			}
		})
	}
}

// ptr returns a pointer to v, for the Config.Local override.
func ptr[T any](v T) *T { return &v }
