package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	dbpkg "github.com/svkexe/platform/internal/db"
)

// authedAdminRequest creates a request with admin user headers.
func authedAdminRequest(method, path string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("X-ExeDev-Userid", "admin1")
	r.Header.Set("X-ExeDev-Email", "admin@example.com")
	return r
}

// newTestServerWithAdmin creates a test server with a seeded admin user.
func newTestServerWithAdmin(t *testing.T) (*Server, *dbpkg.DB) {
	t.Helper()
	srv, database := newTestServer(t)
	_ = database.CreateUser(&dbpkg.User{ID: "admin1", Email: "admin@example.com", Role: "admin"})
	return srv, database
}

func TestGetMe(t *testing.T) {
	srv, _ := newTestServer(t)
	r := authedRequest(http.MethodGet, "/api/me", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var user dbpkg.User
	if err := json.NewDecoder(w.Body).Decode(&user); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if user.ID != "user1" {
		t.Errorf("want user ID 'user1', got %q", user.ID)
	}
}

func TestGetMe_Unauthorized(t *testing.T) {
	srv, _ := newTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", w.Code)
	}
}

func TestAutoProvisioning(t *testing.T) {
	srv, database := newTestServer(t)

	// Request with a new user ID not in DB.
	r := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	r.Header.Set("X-ExeDev-Userid", "newuser42")
	r.Header.Set("X-ExeDev-Email", "new@example.com")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 after auto-provision, got %d: %s", w.Code, w.Body.String())
	}

	// Verify the user was created in the DB.
	u, err := database.GetUserByID("newuser42")
	if err != nil {
		t.Fatalf("user should exist after auto-provisioning: %v", err)
	}
	if u.Email != "new@example.com" {
		t.Errorf("want email 'new@example.com', got %q", u.Email)
	}
	if u.Role != "user" {
		t.Errorf("want role 'user', got %q", u.Role)
	}
}

func TestAdminListUsers(t *testing.T) {
	srv, _ := newTestServerWithAdmin(t)
	r := authedAdminRequest(http.MethodGet, "/api/admin/users")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var users []*dbpkg.User
	if err := json.NewDecoder(w.Body).Decode(&users); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// At minimum user1 (from newTestServer) and admin1 should exist.
	if len(users) < 2 {
		t.Errorf("want at least 2 users, got %d", len(users))
	}
}

func TestAdminListUsers_NonAdminForbidden(t *testing.T) {
	srv, _ := newTestServerWithAdmin(t)
	// user1 has role "user", not "admin".
	r := authedRequest(http.MethodGet, "/api/admin/users", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d", w.Code)
	}
}

func TestAdminListContainers(t *testing.T) {
	srv, database := newTestServerWithAdmin(t)
	_ = database.CreateContainer(&dbpkg.Container{
		ID: "c-admin-test", Name: "box", OwnerID: "user1",
		IncusName: "incus-box", Status: "running",
	})

	r := authedAdminRequest(http.MethodGet, "/api/admin/containers")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var containers []*dbpkg.Container
	if err := json.NewDecoder(w.Body).Decode(&containers); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(containers) != 1 {
		t.Errorf("want 1 container, got %d", len(containers))
	}
}

func TestAdminDeleteUser(t *testing.T) {
	srv, database := newTestServerWithAdmin(t)

	// Create another user to delete.
	_ = database.CreateUser(&dbpkg.User{ID: "todelete", Email: "del@example.com", Role: "user"})

	r := authedAdminRequest(http.MethodDelete, "/api/admin/users/todelete")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", w.Code, w.Body.String())
	}

	// Confirm user is gone from DB.
	_, err := database.GetUserByID("todelete")
	if err == nil {
		t.Error("user should have been deleted")
	}
}

func TestAdminDeleteUser_NotFound(t *testing.T) {
	srv, _ := newTestServerWithAdmin(t)
	r := authedAdminRequest(http.MethodDelete, "/api/admin/users/nonexistent")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", w.Code)
	}
}
