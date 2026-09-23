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

// detailFixture is one VM, its owner, a member it is shared with and a
// stranger, with a router per user so each test reads as the person acting.
type detailFixture struct {
	db       *db.DB
	owner    *chi.Mux
	member   *chi.Mux
	stranger *chi.Mux
}

func newDetailFixture(t *testing.T) detailFixture {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	d, err := NewDashboard(database, nil, nil, "example.com", []byte("01234567890123456789012345678901"), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	as := func(id, email string) *chi.Mux {
		u, err := database.EnsureUser(id, email)
		if err != nil {
			t.Fatal(err)
		}
		r := chi.NewRouter()
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), ctxkeys.User, u)))
			})
		})
		d.RegisterRoutes(r)
		return r
	}
	f := detailFixture{db: database, owner: as("owner", "owner@example.com"), member: as("member", "member@example.com"), stranger: as("stranger", "stranger@example.com")}
	if err := database.CreateContainer(&db.Container{
		ID: "vm", Name: "box", OwnerID: "owner", IncusName: "svkexe-owner-box", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO container_access(container_id,user_id) VALUES('vm','member')`); err != nil {
		t.Fatal(err)
	}
	return f
}

func getAs(router *chi.Mux, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// The owner's page carries every tab and the controls behind them.
func TestVMDetailShowsTheOwnerEveryTab(t *testing.T) {
	f := newDetailFixture(t)
	rec := getAs(f.owner, "/vms/vm")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`data-tab="network"`, `data-tab="access"`, `data-tab="settings"`,
		"/dashboard/vms/vm/publish", "/dashboard/vms/vm/nesting", "/dashboard/vms/vm/access",
		"member@example.com", "https://agent-box.example.com/",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("owner page does not render %q", want)
		}
	}
}

// Someone the VM was shared with may use it, not manage it: the page must not
// hand them the owner's tabs, the member list, or any management action.
func TestVMDetailShowsAMemberOnlyTheOverview(t *testing.T) {
	f := newDetailFixture(t)
	rec := getAs(f.member, "/vms/vm")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, leak := range []string{
		`data-tab="settings"`, `data-tab="access"`, "/dashboard/vms/vm/publish",
		"/dashboard/vms/vm/stop", `hx-delete="/dashboard/vms/vm"`, "member@example.com</span>",
	} {
		if strings.Contains(body, leak) {
			t.Errorf("member page renders owner-only %q", leak)
		}
	}
	// A member reaches the VM by its Incus name; the owner's short name is
	// only an alias inside the owner's own namespace.
	if !strings.Contains(body, "ssh -p 2222 svkexe-owner-box@example.com") {
		t.Error("member page does not show how to reach the VM over SSH")
	}
}

func TestVMDetailRefusesStrangers(t *testing.T) {
	f := newDetailFixture(t)
	for _, path := range []string{"/vms/vm", "/vms/vm/card"} {
		if rec := getAs(f.stranger, path); rec.Code != http.StatusForbidden {
			t.Errorf("%s: status=%d, want 403", path, rec.Code)
		}
	}
	if rec := getAs(f.owner, "/vms/nope"); rec.Code != http.StatusNotFound {
		t.Errorf("missing VM: status=%d, want 404", rec.Code)
	}
}

// The poll answers with the fragment alone, not a page inside a page.
func TestVMCardIsAFragment(t *testing.T) {
	f := newDetailFixture(t)
	rec := getAs(f.owner, "/vms/vm/card")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "<html") || !strings.Contains(body, `id="vm-vm"`) {
		t.Fatalf("not the bare card: %s", body)
	}
}

// Access moved into a tab; the old page address still leads there, and so do
// the form posts that used to land on it.
func TestAccessLandsOnTheAccessTab(t *testing.T) {
	f := newDetailFixture(t)
	rec := getAs(f.owner, "/vms/vm/access")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/dashboard/vms/vm#access" {
		t.Fatalf("status=%d location=%q", rec.Code, rec.Header().Get("Location"))
	}

	// A refused invitation re-renders the page with the error in the Access
	// tab, which the page opens on by itself.
	rec = post(t, f.owner, "/vms/vm/access", url.Values{"email": {"not-an-email"}, "public_key": {"nope"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-initial-tab="access"`) || !strings.Contains(body, `role="alert"`) {
		t.Errorf("refusal is not shown in the Access tab: %s", body)
	}
}
