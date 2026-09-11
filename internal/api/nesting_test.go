package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	dbpkg "github.com/skrashevich/svkexe/internal/db"
)

// The REST reads serialise the Container struct whole, so a field the handler
// never fills comes out as its zero value. NestingAllowed zero reads as "this
// deployment forbids nested containers", which would be a lie on every read.
func TestRESTReadsReportTheRealNestingPolicy(t *testing.T) {
	srv, database := newTestServer(t)
	if err := database.CreateContainer(&dbpkg.Container{
		ID: "vm", Name: "box", OwnerID: "user1", IncusName: "incus-box", Status: "running", Nesting: true,
	}); err != nil {
		t.Fatal(err)
	}

	read := func(t *testing.T, path string) []map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, authedRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", path, rec.Code, rec.Body.String())
		}
		var many []map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &many); err == nil {
			return many
		}
		var one map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &one); err != nil {
			t.Fatalf("%s: %v (%s)", path, err, rec.Body.String())
		}
		return []map[string]any{one}
	}

	for _, path := range []string{"/api/containers", "/api/containers/vm"} {
		got := read(t, path)
		if len(got) == 0 {
			t.Fatalf("%s returned nothing", path)
		}
		for _, c := range got {
			if c["NestingAllowed"] != true {
				t.Errorf("%s reports the deployment as forbidding nesting: %v", path, c["NestingAllowed"])
			}
			if c["Nesting"] != true {
				t.Errorf("%s lost the owner's setting: %v", path, c["Nesting"])
			}
		}
	}

	// And the ceiling actually shows through, rather than the field being
	// hardcoded to something agreeable.
	if err := database.SetNestingAllowed(false); err != nil {
		t.Fatal(err)
	}
	for _, c := range read(t, "/api/containers/vm") {
		if c["NestingAllowed"] != false {
			t.Errorf("a banned deployment still reports nesting as available: %v", c["NestingAllowed"])
		}
		if c["Nesting"] != true {
			t.Error("the ban overwrote the owner's stored wish")
		}
	}
}

// A VM created through REST without the field must come up able to run Docker:
// a client that never heard of the setting must not silently opt out of it.
func TestRESTCreateDefaultsToNesting(t *testing.T) {
	srv, database := newTestServer(t)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, authedRequest(http.MethodPost, "/api/containers", []byte(`{"name":"restbox"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	c, err := database.GetContainerByName("restbox", "user1")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Nesting {
		t.Fatal("a VM created without the field cannot run nested containers")
	}
	if !c.NestingApplied {
		t.Fatal("the instance was built with the setting but not recorded as booting with it")
	}

	// An explicit opt-out is honoured.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, authedRequest(http.MethodPost, "/api/containers", []byte(`{"name":"plainbox","nesting":false}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if c, err = database.GetContainerByName("plainbox", "user1"); err != nil {
		t.Fatal(err)
	}
	if c.Nesting || c.NestingApplied {
		t.Fatalf("an explicit opt-out was ignored: nesting=%v applied=%v", c.Nesting, c.NestingApplied)
	}
}
