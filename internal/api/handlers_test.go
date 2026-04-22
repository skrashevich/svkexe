package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	dbpkg "github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
)

// mockRuntime is a test double for ContainerRuntime.
type mockRuntime struct {
	containers map[string]*runtime.Container
}

func newMockRuntime() *mockRuntime {
	return &mockRuntime{containers: map[string]*runtime.Container{}}
}

func (m *mockRuntime) Create(_ context.Context, opts runtime.CreateOpts) (*runtime.Container, error) {
	c := &runtime.Container{
		ID:      "incus-" + opts.Name,
		Name:    "incus-" + opts.Name,
		Status:  "creating",
		OwnerID: opts.OwnerID,
	}
	m.containers[c.Name] = c
	return c, nil
}

func (m *mockRuntime) Start(_ context.Context, id string) error {
	if c, ok := m.containers[id]; ok {
		c.Status = "running"
	}
	return nil
}

func (m *mockRuntime) Stop(_ context.Context, id string) error {
	if c, ok := m.containers[id]; ok {
		c.Status = "stopped"
	}
	return nil
}

func (m *mockRuntime) Delete(_ context.Context, id string) error {
	delete(m.containers, id)
	return nil
}

func (m *mockRuntime) Get(_ context.Context, id string) (*runtime.Container, error) {
	if c, ok := m.containers[id]; ok {
		return c, nil
	}
	return nil, nil
}

func (m *mockRuntime) List(_ context.Context, ownerID string) ([]*runtime.Container, error) {
	var result []*runtime.Container
	for _, c := range m.containers {
		if ownerID == "" || c.OwnerID == ownerID {
			result = append(result, c)
		}
	}
	return result, nil
}

func (m *mockRuntime) Exec(_ context.Context, id string, cmd []string) ([]byte, error) {
	return []byte("ok"), nil
}

func (m *mockRuntime) Snapshot(_ context.Context, id, name string) error {
	return nil
}

// helpers

var testEncKey = []byte("12345678901234567890123456789012")

// testSessionToken is the session cookie value used by authedRequest. It is
// (re)initialised at the top of every newTestServer / newTestServerWithAdmin
// call, so each test gets a fresh token bound to its own in-memory DB.
var testSessionToken string

func newTestServer(t *testing.T) (*Server, *dbpkg.DB) {
	t.Helper()
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	// Seed a test user.
	_ = database.CreateUser(&dbpkg.User{ID: "user1", Email: "user1@example.com", Role: "user"})
	sess, err := database.CreateSession("user1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	testSessionToken = sess.Token

	srv := NewServer(database, newMockRuntime(), testEncKey, "", nil, nil, nil, nil)
	return srv, database
}

// authedRequest builds a request carrying the current test session cookie.
func authedRequest(method, path string, body []byte) *http.Request {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: testSessionToken})
	return r
}

// --- Tests ---

func TestAuthMiddleware_Missing(t *testing.T) {
	srv, _ := newTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/api/containers", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", w.Code)
	}
}

func TestListContainers_Empty(t *testing.T) {
	srv, _ := newTestServer(t)
	r := authedRequest(http.MethodGet, "/api/containers", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}
}

func TestCreateContainer(t *testing.T) {
	srv, _ := newTestServer(t)
	body, _ := json.Marshal(map[string]any{
		"name":  "mybox",
		"image": "shelley/base",
	})
	r := authedRequest(http.MethodPost, "/api/containers", body)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Errorf("want 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateContainer_MissingName(t *testing.T) {
	srv, _ := newTestServer(t)
	body, _ := json.Marshal(map[string]any{"image": "shelley/base"})
	r := authedRequest(http.MethodPost, "/api/containers", body)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestGetContainer_NotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	r := authedRequest(http.MethodGet, "/api/containers/nonexistent", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", w.Code)
	}
}

func TestOwnershipMiddleware_Forbidden(t *testing.T) {
	srv, database := newTestServer(t)

	// Create another user's container in DB directly.
	_ = database.CreateUser(&dbpkg.User{ID: "other", Email: "other@example.com", Role: "user"})
	_ = database.CreateContainer(&dbpkg.Container{
		ID: "c-other", Name: "box", OwnerID: "other",
		IncusName: "incus-box", Status: "running",
	})

	r := authedRequest(http.MethodGet, "/api/containers/c-other", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("want 403, got %d", w.Code)
	}
}

func TestDeleteContainer(t *testing.T) {
	srv, database := newTestServer(t)

	_ = database.CreateContainer(&dbpkg.Container{
		ID: "c1", Name: "box", OwnerID: "user1",
		IncusName: "incus-mybox", Status: "stopped",
	})
	srv.runtime.(*mockRuntime).containers["incus-mybox"] = &runtime.Container{
		ID: "incus-mybox", Name: "incus-mybox", OwnerID: "user1",
	}

	r := authedRequest(http.MethodDelete, "/api/containers/c1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Errorf("want 204, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateAndListKeys(t *testing.T) {
	srv, _ := newTestServer(t)

	body, _ := json.Marshal(map[string]string{"provider": "openai", "key": "sk-test"})
	r := authedRequest(http.MethodPost, "/api/keys", body)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Errorf("create key: want 201, got %d: %s", w.Code, w.Body.String())
	}

	r2 := authedRequest(http.MethodGet, "/api/keys", nil)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, r2)
	if w2.Code != http.StatusOK {
		t.Errorf("list keys: want 200, got %d", w2.Code)
	}
}
