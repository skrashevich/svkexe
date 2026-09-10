package proxy

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/skrashevich/svkexe/internal/db"
)

func TestSharedAccessCarriesIdentityAndRevokes(t *testing.T) {
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
	req := httptest.NewRequest(http.MethodGet, "https://box.example.com/api/models?share="+link.Token, nil)
	req.Header.Set("X-ExeDev-Userid", "spoofed")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("valid share status=%d", w.Code)
	}
	if got := req.Header.Get("X-ExeDev-Userid"); got != "share:"+link.ID {
		t.Fatalf("identity=%q", got)
	}
	if req.URL.Query().Has("share") {
		t.Fatal("share token forwarded to agent URL")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].Domain != "" {
		t.Fatalf("invalid share cookie: %+v", cookies)
	}
	check := func(host string, want int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "https://"+host+"/api/models", nil)
		r.AddCookie(cookies[0])
		out := httptest.NewRecorder()
		p.ServeHTTP(out, r)
		if out.Code != want {
			t.Fatalf("cookie request status=%d want=%d", out.Code, want)
		}
	}
	check("box.example.com", http.StatusServiceUnavailable)
	check("another.example.com", http.StatusForbidden)
	if err := database.DeleteSharedLinkByToken(link.Token); err != nil {
		t.Fatal(err)
	}
	check("box.example.com", http.StatusForbidden)
}
