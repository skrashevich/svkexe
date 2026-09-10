package proxy

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/skrashevich/svkexe/internal/db"
)

func TestSharedAccessReachesWorkloadAndRevokes(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.EnsureUser("owner", "owner@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := database.CreateContainer(&db.Container{ID: "vm", Name: "box", OwnerID: "owner", IncusName: "box", Status: "stopped"}); err != nil {
		t.Fatal(err)
	}
	link, err := database.CreateSharedLink("vm", "owner", nil)
	if err != nil {
		t.Fatal(err)
	}
	p := New(database, nil, "example.com")
	req := httptest.NewRequest(http.MethodGet, "https://box.example.com/?share="+link.Token, nil)
	req.Header.Set("X-ExeDev-Userid", "spoofed")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)
	// The container is stopped, so reaching the "not running" check means the
	// share was accepted.
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("valid share status=%d", w.Code)
	}
	// The workload authenticates its own users; a spoofed header must not
	// survive and the gateway must not assert an identity of its own.
	if got := req.Header.Get("X-ExeDev-Userid"); got != "" {
		t.Fatalf("identity leaked to workload: %q", got)
	}
	if req.URL.Query().Has("share") {
		t.Fatal("share token forwarded to the workload URL")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].Domain != "" {
		t.Fatalf("invalid share cookie: %+v", cookies)
	}
	check := func(host string, want int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "https://"+host+"/", nil)
		r.AddCookie(cookies[0])
		out := httptest.NewRecorder()
		p.ServeHTTP(out, r)
		if out.Code != want {
			t.Fatalf("%s status=%d want=%d", host, out.Code, want)
		}
	}
	check("box.example.com", http.StatusServiceUnavailable)
	check("another.example.com", http.StatusForbidden)
	// A share is not a shell: it must never open the agent.
	check("agent-box.example.com", http.StatusForbidden)
	if err := database.DeleteSharedLinkByToken(link.Token); err != nil {
		t.Fatal(err)
	}
	check("box.example.com", http.StatusForbidden)
}
