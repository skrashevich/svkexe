package dashboard

import (
	"context"
	"errors"
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

// stubVerifier stands in for the DNS check so no test touches real DNS,
// mirroring internal/api's stub of the same name.
type stubVerifier struct {
	// accept lists the hostnames treated as pointing at this gateway.
	accept map[string]bool
}

func (s *stubVerifier) Verify(_ context.Context, hostname string) error {
	if s.accept[hostname] {
		return nil
	}
	return errors.New("does not point at this gateway")
}

// newAliasDashboard builds a dashboard router with a configured domain and a
// stub verifier. Every request is authenticated as owner; ownership tests use
// a VM owned by someone else to get a 403, the same way publish_handler_test
// does for the publish route.
func newAliasDashboard(t *testing.T) (*chi.Mux, *db.DB, *db.User, *stubVerifier) {
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
	verifier := &stubVerifier{accept: map[string]bool{}}
	d, err := NewDashboard(database, nil, nil, "example.com", []byte("01234567890123456789012345678901"), nil, nil, verifier)
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
	return router, database, owner, verifier
}

func del(t *testing.T, router *chi.Mux, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, path, nil))
	return rec
}

func TestAddAliasVerifiesAndRendersLink(t *testing.T) {
	router, database, owner, verifier := newAliasDashboard(t)
	if err := database.CreateContainer(&db.Container{
		ID: "vm", Name: "box", OwnerID: owner.ID, IncusName: "box", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	verifier.accept["app.example.org"] = true

	rec := post(t, router, "/vms/vm/aliases", url.Values{"hostname": {"app.example.org"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "app.example.org") {
		t.Error("card does not mention the new hostname")
	}
	if !strings.Contains(body, `badge badge-running">verified<`) {
		t.Errorf("card does not mark the alias verified: %s", body)
	}
	if !strings.Contains(body, `href="https://app.example.org/"`) {
		t.Error("a verified alias on a running VM must be linked")
	}
}

func TestAddAliasStoresUnverifiedWithReason(t *testing.T) {
	router, database, owner, _ := newAliasDashboard(t)
	if err := database.CreateContainer(&db.Container{
		ID: "vm", Name: "box", OwnerID: owner.ID, IncusName: "box", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}

	rec := post(t, router, "/vms/vm/aliases", url.Values{"hostname": {"app.example.org"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "does not point at this gateway") {
		t.Errorf("card does not show the failure reason: %s", body)
	}
	if !strings.Contains(body, `badge badge-stopped">unverified<`) {
		t.Errorf("card does not mark the alias unverified: %s", body)
	}

	stored, err := database.ListAliasesByContainer("vm")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].Hostname != "app.example.org" {
		t.Fatalf("want the unverified hostname kept, got %+v", stored)
	}
}

func TestAddAliasRejectsInvalidHostname(t *testing.T) {
	router, database, owner, _ := newAliasDashboard(t)
	if err := database.CreateContainer(&db.Container{
		ID: "vm", Name: "box", OwnerID: owner.ID, IncusName: "box", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}

	rec := post(t, router, "/vms/vm/aliases", url.Values{"hostname": {"not-a-domain"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rec.Code)
	}
	stored, err := database.ListAliasesByContainer("vm")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 0 {
		t.Fatalf("an invalid hostname must not be stored, got %+v", stored)
	}
}

func TestAddAliasRejectsGatewayDomain(t *testing.T) {
	router, database, owner, _ := newAliasDashboard(t)
	if err := database.CreateContainer(&db.Container{
		ID: "vm", Name: "box", OwnerID: owner.ID, IncusName: "box", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}

	// The dashboard's own domain is "example.com" in this fixture; a name
	// under it is already routed by subdomain and would shadow a VM host.
	rec := post(t, router, "/vms/vm/aliases", url.Values{"hostname": {"sub.example.com"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rec.Code)
	}
	stored, err := database.ListAliasesByContainer("vm")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 0 {
		t.Fatalf("a gateway-domain hostname must not be stored, got %+v", stored)
	}
}

func TestAliasRoutesEnforceOwnership(t *testing.T) {
	router, database, _, verifier := newAliasDashboard(t)
	verifier.accept["theirs.example.org"] = true
	if _, err := database.EnsureUser("intruder", "intruder@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateContainer(&db.Container{
		ID: "vm", Name: "box", OwnerID: "intruder", IncusName: "box", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	// Seed an alias directly, since the dashboard route would not let the
	// authenticated owner create it on someone else's VM.
	existing, err := database.CreateContainerAlias("vm", "theirs.example.org")
	if err != nil {
		t.Fatal(err)
	}

	if rec := post(t, router, "/vms/vm/aliases", url.Values{"hostname": {"grab.example.org"}}); rec.Code != http.StatusForbidden {
		t.Errorf("add: status=%d, want 403", rec.Code)
	}
	if rec := post(t, router, "/vms/vm/aliases/"+existing.ID+"/verify", nil); rec.Code != http.StatusForbidden {
		t.Errorf("verify: status=%d, want 403", rec.Code)
	}
	if rec := del(t, router, "/vms/vm/aliases/"+existing.ID); rec.Code != http.StatusForbidden {
		t.Errorf("delete: status=%d, want 403", rec.Code)
	}

	list, err := database.ListAliasesByContainer("vm")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Hostname != "theirs.example.org" {
		t.Fatalf("a non-owner's requests changed the VM's aliases: %+v", list)
	}
}

func TestVerifyAliasFlipsFromUnverifiedToVerified(t *testing.T) {
	router, database, owner, verifier := newAliasDashboard(t)
	if err := database.CreateContainer(&db.Container{
		ID: "vm", Name: "box", OwnerID: owner.ID, IncusName: "box", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}

	rec := post(t, router, "/vms/vm/aliases", url.Values{"hostname": {"app.example.org"}})
	if strings.Contains(rec.Body.String(), `badge badge-running">verified<`) {
		t.Fatal("alias verified before DNS pointed here")
	}

	alias, err := database.GetVerifiedAliasByHostname("app.example.org")
	if err == nil {
		t.Fatalf("unexpectedly already verified: %+v", alias)
	}
	stored, err := database.ListAliasesByContainer("vm")
	if err != nil || len(stored) != 1 {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}

	// The owner fixes their DNS record and re-checks.
	verifier.accept["app.example.org"] = true
	rec = post(t, router, "/vms/vm/aliases/"+stored[0].ID+"/verify", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `badge badge-running">verified<`) {
		t.Errorf("card still shows the alias unverified: %s", rec.Body.String())
	}
}

func TestRemoveAliasRendersCardWithoutItAndFreesHostname(t *testing.T) {
	router, database, owner, verifier := newAliasDashboard(t)
	if err := database.CreateContainer(&db.Container{
		ID: "vm", Name: "box", OwnerID: owner.ID, IncusName: "box", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	verifier.accept["app.example.org"] = true
	post(t, router, "/vms/vm/aliases", url.Values{"hostname": {"app.example.org"}})

	stored, err := database.ListAliasesByContainer("vm")
	if err != nil || len(stored) != 1 {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}

	rec := del(t, router, "/vms/vm/aliases/"+stored[0].ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: status=%d body=%s", rec.Code, rec.Body.String())
	}
	// The add form's placeholder text is itself "app.example.org", so check
	// for the hostname as rendered in an alias row, not anywhere in the body.
	if strings.Contains(rec.Body.String(), ">app.example.org<") {
		t.Errorf("card still shows the removed hostname: %s", rec.Body.String())
	}

	after, err := database.ListAliasesByContainer("vm")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("alias survived deletion: %+v", after)
	}

	// A freed hostname must be claimable again.
	if rec := post(t, router, "/vms/vm/aliases", url.Values{"hostname": {"app.example.org"}}); rec.Code != http.StatusOK {
		t.Fatalf("re-claim after delete: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
