package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	dbpkg "github.com/skrashevich/svkexe/internal/db"
)

func TestRevokeSharedLink_Ownership(t *testing.T) {
	srv, database := newTestServer(t)
	_ = database.CreateUser(&dbpkg.User{ID: "other", Email: "other@example.com", Role: "user"})
	_ = database.CreateContainer(&dbpkg.Container{
		ID: "c-other", Name: "box", OwnerID: "other",
		IncusName: "incus-other", Status: "running",
	})

	link, err := database.CreateSharedLink("c-other", "other", nil)
	if err != nil {
		t.Fatalf("create shared link: %v", err)
	}

	r := authedRequest(http.MethodDelete, "/api/shares/"+link.Token, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("revoke other user's share: want 403, got %d: %s", w.Code, w.Body.String())
	}

	got, err := database.LookupSharedLink(link.Token)
	if err != nil {
		t.Fatalf("link should still exist: %v", err)
	}
	if got.Token != link.Token {
		t.Fatalf("unexpected link token %q", got.Token)
	}
}
