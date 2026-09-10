package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skrashevich/svkexe/internal/ctxkeys"
	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/updater"
	"github.com/skrashevich/svkexe/internal/version"
)

// Full SHAs, so the checker treats the local build as stamped and compares it
// against the stub's answer instead of reporting a dev build.
const (
	localCommit  = "1111111111111111111111111111111111111111"
	remoteCommit = "abcdef0123456789abcdef0123456789abcdef01"
)

func testVersionInfo() version.Info {
	return version.Info{
		Version:         "v9.9.9",
		Commit:          localCommit,
		BuildDate:       "2026-01-01T00:00:00Z",
		PicoClawVersion: "0.4.2",
		ShelleyCommit:   "fedcba9",
	}
}

// newSystemRouter builds a dashboard router whose requests are authenticated as
// a user with the given role.
func newSystemRouter(t *testing.T, upd *updater.Service, role string) *chi.Mux {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	user := &db.User{ID: "u1", Email: "u1@example.com", Role: role}
	if err := database.CreateUser(user); err != nil {
		t.Fatal(err)
	}

	d, err := NewDashboard(database, nil, nil, "example.com", []byte("01234567890123456789012345678901"), nil, upd, nil)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxkeys.User, user)))
		})
	})
	d.RegisterRoutes(router)
	return router
}

// stubGitHub answers the branch-commit endpoint the checker calls. status other
// than 200 makes every request fail, which is how the failed-check state is
// exercised.
func stubGitHub(t *testing.T, sha string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if status != http.StatusOK {
			http.Error(w, "rate limit exceeded", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"sha":%q,"html_url":"https://github.com/skrashevich/svkexe/commit/%s","commit":{"committer":{"date":"2026-01-02T03:04:05Z"}}}`, sha, sha)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newTestUpdater wires a service against a stub API and a runner rooted at dir.
func newTestUpdater(t *testing.T, apiBase, dir string) *updater.Service {
	t.Helper()
	// Stand in for the marker scripts/install-update-units.sh writes once the
	// root-owned svkexe-update.path unit is enabled; without it the runner
	// correctly refuses to write a trigger nothing would consume.
	if err := os.WriteFile(filepath.Join(dir, "update-watcher"), []byte("svkexe-update.path\n"), 0o644); err != nil {
		t.Fatalf("write watcher marker: %v", err)
	}
	local := testVersionInfo()
	return updater.NewService(
		updater.Config{APIBase: apiBase, Local: &local},
		updater.RunnerConfig{
			TriggerPath: filepath.Join(dir, "update.trigger"),
			StatusPath:  filepath.Join(dir, "update-status.json"),
			WatcherPath: filepath.Join(dir, "update-watcher"),
		},
	)
}

func get(t *testing.T, router *chi.Mux, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// writeRunState seeds the status file the runner reads.
func writeRunState(t *testing.T, dir string, st updater.RunState) {
	t.Helper()
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "update-status.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSystemPageShowsComponentVersions(t *testing.T) {
	dir := t.TempDir()
	router := newSystemRouter(t, newTestUpdater(t, stubGitHub(t, remoteCommit, http.StatusOK).URL, dir), "admin")

	rec := get(t, router, "/system")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"<h1>System</h1>", "v9.9.9", localCommit, "0.4.2", "fedcba9", "Check for updates"} {
		if !strings.Contains(body, want) {
			t.Errorf("system page does not mention %q", want)
		}
	}
}

func TestSystemRoutesAreAdminOnly(t *testing.T) {
	dir := t.TempDir()
	router := newSystemRouter(t, newTestUpdater(t, stubGitHub(t, remoteCommit, http.StatusOK).URL, dir), "user")

	cases := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/system"},
		{http.MethodGet, "/system/check"},
		{http.MethodPost, "/system/update"},
		{http.MethodGet, "/system/status"},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: status=%d, want 403", tc.method, tc.path, rec.Code)
		}
	}
	// The trigger must not have been written by the rejected POST.
	if _, err := os.Stat(filepath.Join(dir, "update.trigger")); !os.IsNotExist(err) {
		t.Fatal("a non-admin started an update")
	}
}

func TestUpdateCheckReportsAvailableUpdate(t *testing.T) {
	router := newSystemRouter(t, newTestUpdater(t, stubGitHub(t, remoteCommit, http.StatusOK).URL, t.TempDir()), "admin")

	rec := get(t, router, "/system/check")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Update available") {
		t.Errorf("fragment does not announce the update: %s", body)
	}
	if !strings.Contains(body, remoteCommit[:7]) {
		t.Errorf("fragment does not show the remote short sha: %s", body)
	}
	if !strings.Contains(body, `hx-post="/dashboard/system/update"`) {
		t.Errorf("fragment offers no way to install the update: %s", body)
	}
}

func TestUpdateCheckReportsUpToDate(t *testing.T) {
	router := newSystemRouter(t, newTestUpdater(t, stubGitHub(t, localCommit, http.StatusOK).URL, t.TempDir()), "admin")

	rec := get(t, router, "/system/check")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Up to date") {
		t.Errorf("fragment does not report an up-to-date build: %s", body)
	}
	if strings.Contains(body, `hx-post="/dashboard/system/update"`) {
		t.Error("fragment offers an update against an identical commit")
	}
}

// The check runs inside an htmx fragment, so a GitHub failure has to arrive as
// renderable content; a 500 here would only produce an error toast.
func TestUpdateCheckRendersFailureAsContent(t *testing.T) {
	router := newSystemRouter(t, newTestUpdater(t, stubGitHub(t, remoteCommit, http.StatusInternalServerError).URL, t.TempDir()), "admin")

	rec := get(t, router, "/system/check")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Update check failed") {
		t.Errorf("fragment hides the failure: %s", rec.Body.String())
	}
}

// The deployment that matters here is the one the reviewers found: a writable
// data directory with no svkexe-update.path unit watching it. The button must
// come back disabled and explain itself instead of writing a trigger that
// nothing will ever consume.
func TestUpdateStartReportsUnavailableDeployment(t *testing.T) {
	dir := t.TempDir()
	local := testVersionInfo()
	upd := updater.NewService(
		updater.Config{APIBase: stubGitHub(t, remoteCommit, http.StatusOK).URL, Local: &local},
		updater.RunnerConfig{
			TriggerPath: filepath.Join(dir, "update.trigger"),
			StatusPath:  filepath.Join(dir, "update-status.json"),
			WatcherPath: filepath.Join(dir, "update-watcher"), // deliberately never written
		},
	)
	router := newSystemRouter(t, upd, "admin")

	ok, reason := upd.Available()
	if ok {
		t.Fatal("runner claims a deployment with no watcher is usable")
	}
	if !strings.Contains(reason, "no update watcher is installed") {
		t.Errorf("reason = %q, want it to name the missing watcher", reason)
	}

	rec := post(t, router, "/system/update", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, reason) {
		t.Errorf("fragment does not explain %q: %s", reason, body)
	}
	if !strings.Contains(body, "<button class=\"btn btn-primary\" disabled>") {
		t.Errorf("fragment leaves the update button enabled: %s", body)
	}
}

func TestUpdateStartWritesTrigger(t *testing.T) {
	dir := t.TempDir()
	router := newSystemRouter(t, newTestUpdater(t, stubGitHub(t, remoteCommit, http.StatusOK).URL, dir), "admin")

	rec := post(t, router, "/system/update", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "update.trigger")); err != nil {
		t.Fatalf("trigger not written: %v", err)
	}
	// Start seeds a running state, so the returned fragment must already poll.
	if !strings.Contains(rec.Body.String(), "every 3s") {
		t.Errorf("fragment does not poll for progress: %s", rec.Body.String())
	}
}

func TestUpdateStatusPollsOnlyWhileRunning(t *testing.T) {
	dir := t.TempDir()
	router := newSystemRouter(t, newTestUpdater(t, stubGitHub(t, remoteCommit, http.StatusOK).URL, dir), "admin")

	writeRunState(t, dir, updater.RunState{
		State:     updater.StateRunning,
		StartedAt: time.Now().UTC(),
		Log:       "building gateway\n",
	})
	rec := get(t, router, "/system/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-trigger="every 3s"`) {
		t.Errorf("running state does not poll: %s", body)
	}
	if !strings.Contains(body, "building gateway") {
		t.Errorf("running state hides the log tail: %s", body)
	}

	writeRunState(t, dir, updater.RunState{
		State:      updater.StateSuccess,
		StartedAt:  time.Now().Add(-time.Minute).UTC(),
		FinishedAt: time.Now().UTC(),
		Commit:     remoteCommit,
	})
	rec = get(t, router, "/system/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	if strings.Contains(body, "every 3s") {
		t.Errorf("a finished update keeps polling forever: %s", body)
	}
	if !strings.Contains(body, "Update finished") {
		t.Errorf("success state not rendered: %s", body)
	}
}

// A gateway wired without an updater still has to serve the page.
func TestSystemPageWithoutUpdater(t *testing.T) {
	router := newSystemRouter(t, nil, "admin")

	rec := get(t, router, "/system")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "without the update service") {
		t.Errorf("page does not explain why it cannot update: %s", rec.Body.String())
	}

	rec = post(t, router, "/system/update", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Could not start the update") {
		t.Errorf("update fragment does not refuse cleanly: %s", rec.Body.String())
	}
}
