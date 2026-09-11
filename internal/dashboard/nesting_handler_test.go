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
	"github.com/skrashevich/svkexe/internal/runtime"
)

// nestingRuntime is enough runtime for the settings and restart paths: they
// only stop, start, look up and configure.
type nestingRuntime struct {
	runtime.ContainerRuntime
	nesting  map[string]bool
	stopped  int
	started  int
	setCalls int
}

func newNestingRuntime() *nestingRuntime {
	return &nestingRuntime{nesting: map[string]bool{}}
}

func (n *nestingRuntime) SetNesting(_ context.Context, id string, enabled bool) error {
	n.setCalls++
	n.nesting[id] = enabled
	return nil
}
func (n *nestingRuntime) Create(_ context.Context, opts runtime.CreateOpts) (*runtime.Container, error) {
	name := "incus-" + opts.Name
	n.nesting[name] = opts.Nesting
	return &runtime.Container{ID: name, Name: name, Status: "stopped", OwnerID: opts.OwnerID}, nil
}
func (n *nestingRuntime) Start(context.Context, string) error { n.started++; return nil }
func (n *nestingRuntime) Stop(context.Context, string) error  { n.stopped++; return nil }
func (n *nestingRuntime) Get(_ context.Context, id string) (*runtime.Container, error) {
	return &runtime.Container{ID: id, Name: id, Status: "running", IP: "10.0.0.5"}, nil
}
func (n *nestingRuntime) Exec(context.Context, string, []string) ([]byte, error) {
	return []byte(""), nil
}

func newNestingDashboard(t *testing.T, role string) (*chi.Mux, *db.DB, *nestingRuntime) {
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
	owner.Role = role

	rt := newNestingRuntime()
	d, err := NewDashboard(database, rt, nil, "example.com", []byte("01234567890123456789012345678901"), nil, nil, nil)
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
	return router, database, rt
}

func nestingVM(t *testing.T, database *db.DB, ownerID, status string, wants bool) {
	t.Helper()
	if err := database.CreateContainer(&db.Container{
		ID: "vm", Name: "box", OwnerID: ownerID, IncusName: "incus-box", Status: status, Nesting: wants,
	}); err != nil {
		t.Fatal(err)
	}
}

// The owner decides this for their own VM, and the change reaches the runtime
// straight away so that a start from anywhere — the SSH menu, the REST API —
// picks it up.
func TestOwnerTurnsNestingOffForTheirVM(t *testing.T) {
	router, database, rt := newNestingDashboard(t, "user")
	nestingVM(t, database, "owner", "running", true)

	// An absent checkbox is how a browser reports "unchecked".
	rec := post(t, router, "/vms/vm/nesting", url.Values{})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	c, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if c.Nesting {
		t.Fatal("unchecking the box did not turn nesting off")
	}
	if rt.setCalls == 0 || rt.nesting["incus-box"] {
		t.Fatalf("the runtime was not told: calls=%d value=%v", rt.setCalls, rt.nesting["incus-box"])
	}

	// And back on again.
	if rec := post(t, router, "/vms/vm/nesting", url.Values{"nesting": {"1"}}); rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if c, err = database.GetContainerByID("vm"); err != nil {
		t.Fatal(err)
	}
	if !c.Nesting {
		t.Fatal("re-checking the box did not turn nesting back on")
	}
}

// The card is the only place that says the setting is not live yet, so it has
// to say it while the VM runs and stop saying it once the VM has booted with it.
func TestTheCardAsksForTheRestartItIsOwed(t *testing.T) {
	router, database, _ := newNestingDashboard(t, "user")
	nestingVM(t, database, "owner", "running", true)

	body := post(t, router, "/vms/vm/nesting", url.Values{"nesting": {"1"}}).Body.String()
	if !strings.Contains(body, "Restart this VM to apply") {
		t.Errorf("the card does not ask for a restart: %s", body)
	}
	if !strings.Contains(body, "/dashboard/vms/vm/restart") {
		t.Error("the card offers no way to restart")
	}

	if err := database.SetNestingApplied("vm", true); err != nil {
		t.Fatal(err)
	}
	body = post(t, router, "/vms/vm/nesting", url.Values{"nesting": {"1"}}).Body.String()
	if strings.Contains(body, "Restart this VM to apply") {
		t.Error("a VM that booted with the setting still asks for a restart")
	}
}

// A restart is what makes the setting real, so it has to record it: otherwise
// the card would keep asking for a restart that already happened.
func TestRestartAppliesTheSetting(t *testing.T) {
	router, database, rt := newNestingDashboard(t, "user")
	nestingVM(t, database, "owner", "running", true)

	rec := post(t, router, "/vms/vm/restart", url.Values{})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rt.stopped != 1 || rt.started != 1 {
		t.Fatalf("stopped=%d started=%d", rt.stopped, rt.started)
	}
	c, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if !c.NestingApplied {
		t.Fatal("the restart did not record the setting it booted with")
	}
	if strings.Contains(rec.Body.String(), "Restart this VM to apply") {
		t.Error("the card still asks for a restart right after one")
	}
}

// The deployment-wide switch is a ceiling: while it is off, no owner may hand
// their VM the capability, and the form that would do it is not offered.
func TestTheCeilingBlocksTheOwnersSwitch(t *testing.T) {
	router, database, rt := newNestingDashboard(t, "user")
	nestingVM(t, database, "owner", "running", false)
	if err := database.SetNestingAllowed(false); err != nil {
		t.Fatal(err)
	}

	rec := post(t, router, "/vms/vm/nesting", url.Values{"nesting": {"1"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", rec.Code)
	}
	c, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if c.Nesting {
		t.Fatal("an owner enabled nesting on a deployment that forbids it")
	}
	if rt.setCalls != 0 {
		t.Fatal("a refused change still reached the runtime")
	}

	// The card renders the switch out of reach rather than hiding the answer.
	body := post(t, router, "/vms/vm/publish", url.Values{"app_port": {"3000"}}).Body.String()
	if !strings.Contains(body, "disabled>") {
		t.Errorf("the card offers a switch the deployment forbids: %s", body)
	}
	if !strings.Contains(body, "Turned off for this whole deployment") {
		t.Errorf("the card does not explain why nesting is unavailable: %s", body)
	}
}

func TestNestingEnforcesOwnership(t *testing.T) {
	router, database, _ := newNestingDashboard(t, "user")
	if _, err := database.EnsureUser("intruder", "intruder@example.com"); err != nil {
		t.Fatal(err)
	}
	nestingVM(t, database, "intruder", "running", false)

	for _, path := range []string{"/vms/vm/nesting", "/vms/vm/restart"} {
		if rec := post(t, router, path, url.Values{"nesting": {"1"}}); rec.Code != http.StatusForbidden {
			t.Errorf("%s: status=%d, want 403", path, rec.Code)
		}
	}
	c, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if c.Nesting {
		t.Fatal("a non-owner changed someone else's VM")
	}
}

// The ceiling belongs to the operator; a tenant reaching the endpoint directly
// must not be able to lift it for the whole platform.
func TestOnlyAnAdminMovesTheCeiling(t *testing.T) {
	router, database, _ := newNestingDashboard(t, "user")
	if err := database.SetNestingAllowed(false); err != nil {
		t.Fatal(err)
	}

	if rec := post(t, router, "/system/nesting", url.Values{"nesting_allowed": {"1"}}); rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", rec.Code)
	}
	allowed, err := database.NestingAllowed()
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("a tenant lifted the deployment-wide ban")
	}
}

func TestAdminMovesTheCeiling(t *testing.T) {
	router, database, _ := newNestingDashboard(t, "admin")

	rec := post(t, router, "/system/nesting", url.Values{})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	allowed, err := database.NestingAllowed()
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("an unchecked box left nesting allowed")
	}
	if !strings.Contains(rec.Body.String(), "No VM can run containers of its own") {
		t.Errorf("the fragment does not reflect the new state: %s", rec.Body.String())
	}

	if rec := post(t, router, "/system/nesting", url.Values{"nesting_allowed": {"1"}}); rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if allowed, err = database.NestingAllowed(); err != nil || !allowed {
		t.Fatalf("allowed=%v err=%v", allowed, err)
	}
}

// A VM created from the dashboard has to come up able to run Docker without the
// owner having to find a setting first.
func TestCreatedVMsGetNestingByDefault(t *testing.T) {
	router, database, _ := newNestingDashboard(t, "user")

	if rec := post(t, router, "/vms", url.Values{"name": {"fresh"}, "nesting": {"1"}}); rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	c, err := database.GetContainerByName("fresh", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Nesting {
		t.Fatal("a VM created with the box ticked cannot run nested containers")
	}

	// The create form offers the switch, ticked, so the default is visible
	// rather than implied.
	req := httptest.NewRequest(http.MethodGet, "/vms/create", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `name="nesting" value="1" checked`) {
		t.Errorf("the create form does not offer nesting by default: %s", rec.Body.String())
	}
}

// While the deployment forbids nesting the create form has no switch, and the
// missing field must not be read as the owner declining it: lifting the ban
// should give those VMs nesting on their next start.
func TestCreateKeepsTheDefaultWishWhileTheCeilingIsDown(t *testing.T) {
	router, database, _ := newNestingDashboard(t, "user")
	if err := database.SetNestingAllowed(false); err != nil {
		t.Fatal(err)
	}

	if rec := post(t, router, "/vms", url.Values{"name": {"fresh"}}); rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	c, err := database.GetContainerByName("fresh", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Nesting {
		t.Fatal("a VM created under the ban was recorded as not wanting nesting")
	}
	if err := database.AttachNestingPolicy(c); err != nil {
		t.Fatal(err)
	}
	if c.NestingEffective() {
		t.Fatal("the ban did not hold for a VM created under it")
	}
}

// failingStartRuntime accepts everything except the start itself.
type failingStartRuntime struct {
	nestingRuntime
}

func (f *failingStartRuntime) Start(context.Context, string) error {
	return errors.New("incus refused to start the instance")
}

// A start that did not happen must not leave the row saying "running": the card
// speaks from that row, and would report the nesting setting it was just given
// as live on a VM that is actually down.
func TestAFailedStartClearsTheStaleRunningRow(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	owner, err := database.EnsureUser("owner", "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	rt := &failingStartRuntime{nestingRuntime: *newNestingRuntime()}
	d, err := NewDashboard(database, rt, nil, "example.com", []byte("01234567890123456789012345678901"), nil, nil, nil)
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

	// The row still claims "running" from an earlier life, which is the state
	// that makes the stale answer visible.
	if err := database.CreateContainer(&db.Container{
		ID: "vm", Name: "box", OwnerID: owner.ID, IncusName: "incus-box", Status: "running", Nesting: true,
	}); err != nil {
		t.Fatal(err)
	}

	if rec := post(t, router, "/vms/vm/start", url.Values{}); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500", rec.Code)
	}
	c, err := database.GetContainerByID("vm")
	if err != nil {
		t.Fatal(err)
	}
	if c.Status == "running" {
		t.Fatal("a VM that failed to start is still reported as running")
	}
	if err := database.AttachNestingPolicy(c); err != nil {
		t.Fatal(err)
	}
	if c.NestingPending() {
		t.Error("a VM that is not running is asking for a restart")
	}
}
