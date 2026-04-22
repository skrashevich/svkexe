package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	dbpkg "github.com/svkexe/platform/internal/db"
)

// newAuthTestServer creates a server with a single user who has a known
// bcrypt-hashed password. It returns the server and the user ID.
func newAuthTestServer(t *testing.T, email, password string, role string) (*Server, *dbpkg.DB, string) {
	t.Helper()
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	u := &dbpkg.User{
		ID:           "u-" + email,
		Email:        email,
		Role:         role,
		PasswordHash: string(hash),
	}
	if err := database.CreateUser(u); err != nil {
		t.Fatalf("create user: %v", err)
	}

	srv := NewServer(database, newMockRuntime(), testEncKey, "", nil, nil, nil, nil)
	return srv, database, u.ID
}

func TestLogin_Success(t *testing.T) {
	srv, _, _ := newAuthTestServer(t, "alice@example.com", "correct-horse", "user")

	form := url.Values{}
	form.Set("email", "alice@example.com")
	form.Set("password", "correct-horse")

	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d: %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	var token string
	for _, c := range cookies {
		if c.Name == SessionCookieName {
			token = c.Value
		}
	}
	if token == "" {
		t.Fatalf("missing %s cookie in response", SessionCookieName)
	}

	// Use the cookie on a protected endpoint.
	r2 := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	r2.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, r2)

	if w2.Code != http.StatusOK {
		t.Fatalf("/api/me with valid session: want 200, got %d: %s", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), `"Email":"alice@example.com"`) {
		t.Errorf("me response missing email: %s", w2.Body.String())
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	srv, _, _ := newAuthTestServer(t, "bob@example.com", "right-password", "user")

	form := url.Values{}
	form.Set("email", "bob@example.com")
	form.Set("password", "wrong-password")

	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == SessionCookieName && c.Value != "" {
			t.Errorf("unexpected session cookie issued on failed login")
		}
	}
}

func TestLogin_UnknownEmail(t *testing.T) {
	srv, _, _ := newAuthTestServer(t, "carol@example.com", "hunter2", "user")

	form := url.Values{}
	form.Set("email", "ghost@example.com")
	form.Set("password", "anything")

	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestLogout_InvalidatesSession(t *testing.T) {
	srv, database, userID := newAuthTestServer(t, "dave@example.com", "pw12345678", "user")

	sess, err := database.CreateSession(userID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	r := httptest.NewRequest(http.MethodPost, "/logout", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sess.Token})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303 on logout, got %d", w.Code)
	}

	// Session should now be gone.
	r2 := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	r2.AddCookie(&http.Cookie{Name: SessionCookieName, Value: sess.Token})
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, r2)
	if w2.Code != http.StatusUnauthorized {
		t.Fatalf("post-logout /api/me: want 401, got %d", w2.Code)
	}
}

func TestRegister_FirstUserBecomesAdmin(t *testing.T) {
	database, err := dbpkg.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	srv := NewServer(database, newMockRuntime(), testEncKey, "", nil, nil, nil, nil)

	form := url.Values{}
	form.Set("email", "first@example.com")
	form.Set("password", "strong-password-123")
	form.Set("password_confirm", "strong-password-123")

	r := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303 from /register, got %d: %s", w.Code, w.Body.String())
	}

	user, err := database.GetUserByEmail("first@example.com")
	if err != nil {
		t.Fatalf("user not created: %v", err)
	}
	if user.Role != "admin" {
		t.Errorf("first user should be admin, got role=%q", user.Role)
	}

	// Second /register attempt must be rejected (redirect to /login).
	form2 := url.Values{}
	form2.Set("email", "second@example.com")
	form2.Set("password", "another-password")
	form2.Set("password_confirm", "another-password")
	r2 := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(form2.Encode()))
	r2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, r2)
	if w2.Code != http.StatusSeeOther {
		t.Fatalf("second /register: want 303 redirect, got %d", w2.Code)
	}
	if _, err := database.GetUserByEmail("second@example.com"); err == nil {
		t.Error("second user should not have been created")
	}
}

func TestAuthMiddleware_HTMLRedirect(t *testing.T) {
	srv, _, _ := newAuthTestServer(t, "redir@example.com", "pass12345", "user")

	// Browser request (Accept: text/html) without cookie should get 303 to /login.
	r := httptest.NewRequest(http.MethodGet, "/dashboard/", nil)
	r.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("want 303 for unauthenticated HTML request, got %d", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "/login") {
		t.Errorf("want redirect to /login, got %q", loc)
	}
}
