package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/skrashevich/svkexe/internal/ctxkeys"
	"github.com/skrashevich/svkexe/internal/db"
)

func newPublishDashboard(t *testing.T) (*chi.Mux, *db.DB, *db.User) {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	owner, err := database.EnsureUser("owner", "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDashboard(database, nil, nil, "example.com", []byte("01234567890123456789012345678901"), nil)
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxkeys.User, owner)))
		})
	})
	d.RegisterRoutes(router)
	return router, database, owner
}

func post(t *testing.T, router *chi.Mux, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestPublishHandlerUpdatesSettings(t *testing.T) {
	router, database, owner := newPublishDashboard(t)
	if err := database.CreateContainer(&db.Container{
		ID: "vm", Name: "box", OwnerID: owner.ID, IncusName: "box", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}

	rec := post(t, router, "/vms/vm/publish", url.Values{"app_port": {"8080"}, "app_public": {"1"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	c, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if c.AppPort != 8080 || !c.AppPublic {
		t.Fatalf("not saved: port=%d public=%v", c.AppPort, c.AppPublic)
	}
	// The re-rendered card must point the two buttons at different hosts.
	body := rec.Body.String()
	if !strings.Contains(body, "https://agent-box.example.com/") {
		t.Error("card lost the agent link")
	}
	if !strings.Contains(body, `href="https://box.example.com/"`) {
		t.Error("card lost the workload link")
	}

	// An absent checkbox is how the browser reports "unchecked".
	if rec := post(t, router, "/vms/vm/publish", url.Values{"app_port": {"8080"}}); rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	c, err = database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if c.AppPublic {
		t.Error("unchecking Public did not make the VM private")
	}
}

func TestPublishHandlerRejectsBadPort(t *testing.T) {
	router, database, owner := newPublishDashboard(t)
	if err := database.CreateContainer(&db.Container{
		ID: "vm", Name: "box", OwnerID: owner.ID, IncusName: "box", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	for _, port := range []string{"", "0", "-1", "70000", "9000", "abc"} {
		rec := post(t, router, "/vms/vm/publish", url.Values{"app_port": {port}})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("port %q accepted: %d", port, rec.Code)
		}
	}
}

func TestPublishHandlerEnforcesOwnership(t *testing.T) {
	router, database, _ := newPublishDashboard(t)
	if _, err := database.EnsureUser("intruder", "intruder@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateContainer(&db.Container{
		ID: "vm", Name: "box", OwnerID: "intruder", IncusName: "box", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	rec := post(t, router, "/vms/vm/publish", url.Values{"app_port": {"8080"}, "app_public": {"1"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", rec.Code)
	}
	c, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if c.AppPublic {
		t.Fatal("a non-owner published someone else's VM")
	}
}

// The dashboard create path had no name validation at all, which would let a
// reserved prefix through and break routing for another VM.
func TestCreateVMRejectsReservedNames(t *testing.T) {
	router, _, _ := newPublishDashboard(t)
	for _, name := range []string{"agent-box", "3000-box", "Box", "b"} {
		rec := post(t, router, "/vms", url.Values{"name": {name}})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("name %q accepted: %d", name, rec.Code)
		}
	}
}

func TestCreateFormShowsBothHosts(t *testing.T) {
	router, _, _ := newPublishDashboard(t)
	req := httptest.NewRequest(http.MethodGet, "/vms/create", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"name.example.com", "agent-name.example.com"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("create form does not mention %s", want)
		}
	}
}
