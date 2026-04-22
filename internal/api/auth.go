package api

import (
	"database/sql"
	"errors"
	"html/template"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	dbpkg "github.com/skrashevich/svkexe/internal/db"
)

// SessionCookieName is the name of the cookie carrying the session token.
const SessionCookieName = "svkexe_session"

// bcryptCost tunes bcrypt work factor — 12 balances security and sub-100 ms
// login latency on modest hardware.
const bcryptCost = 12

// CookieSecure controls whether session cookies carry the Secure attribute.
// Set via the GATEWAY_COOKIE_SECURE env var in main.go. Defaults to false so
// local HTTP testing works; production deployments behind TLS should set it.
var CookieSecure = false

// setSessionCookie writes an HttpOnly, SameSite=Lax session cookie.
func setSessionCookie(w http.ResponseWriter, token string, maxAgeSeconds int) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAgeSeconds,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	setSessionCookie(w, "", -1)
}

// pageData is the root context passed to the inline HTML templates.
type pageData struct {
	Title        string
	Error        string
	AllowRegister bool
	Email        string
}

var loginTmpl = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>{{.Title}}</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
  body{font-family:system-ui,sans-serif;background:#0f1115;color:#eee;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0}
  .card{background:#1a1d24;padding:2rem;border-radius:8px;min-width:320px;box-shadow:0 4px 24px rgba(0,0,0,.4)}
  h1{margin:0 0 1rem;font-size:1.25rem}
  label{display:block;margin-top:.75rem;font-size:.9rem;color:#aaa}
  input{width:100%;box-sizing:border-box;padding:.5rem;margin-top:.25rem;background:#0f1115;color:#eee;border:1px solid #333;border-radius:4px}
  button{margin-top:1.25rem;width:100%;padding:.6rem;background:#3b82f6;color:#fff;border:0;border-radius:4px;font-weight:600;cursor:pointer}
  button:hover{background:#2563eb}
  .err{background:#3a1a1d;color:#f87171;padding:.5rem .75rem;border-radius:4px;margin-bottom:1rem;font-size:.9rem}
  a{color:#93c5fd;text-decoration:none;font-size:.85rem}
  .foot{margin-top:1rem;text-align:center}
</style></head><body>
<form class="card" method="POST" action="/login">
  <h1>{{.Title}}</h1>
  {{if .Error}}<div class="err">{{.Error}}</div>{{end}}
  <label>Email <input type="email" name="email" value="{{.Email}}" autofocus required></label>
  <label>Password <input type="password" name="password" required></label>
  <button type="submit">Sign in</button>
  {{if .AllowRegister}}<div class="foot"><a href="/register">Create the first admin account</a></div>{{end}}
</form>
</body></html>`))

var registerTmpl = template.Must(template.New("register").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>{{.Title}}</title>
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>
  body{font-family:system-ui,sans-serif;background:#0f1115;color:#eee;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0}
  .card{background:#1a1d24;padding:2rem;border-radius:8px;min-width:340px;box-shadow:0 4px 24px rgba(0,0,0,.4)}
  h1{margin:0 0 .25rem;font-size:1.25rem}
  .sub{margin:0 0 1rem;color:#888;font-size:.85rem}
  label{display:block;margin-top:.75rem;font-size:.9rem;color:#aaa}
  input{width:100%;box-sizing:border-box;padding:.5rem;margin-top:.25rem;background:#0f1115;color:#eee;border:1px solid #333;border-radius:4px}
  button{margin-top:1.25rem;width:100%;padding:.6rem;background:#10b981;color:#fff;border:0;border-radius:4px;font-weight:600;cursor:pointer}
  button:hover{background:#059669}
  .err{background:#3a1a1d;color:#f87171;padding:.5rem .75rem;border-radius:4px;margin-bottom:1rem;font-size:.9rem}
</style></head><body>
<form class="card" method="POST" action="/register">
  <h1>{{.Title}}</h1>
  <p class="sub">No users exist yet — this account becomes the admin.</p>
  {{if .Error}}<div class="err">{{.Error}}</div>{{end}}
  <label>Email <input type="email" name="email" value="{{.Email}}" autofocus required></label>
  <label>Password (min 8 chars) <input type="password" name="password" minlength="8" required></label>
  <label>Confirm password <input type="password" name="password_confirm" minlength="8" required></label>
  <button type="submit">Create admin account</button>
</form>
</body></html>`))

// loginGet renders the login form.
func (s *Server) loginGet(w http.ResponseWriter, r *http.Request) {
	allow, _ := s.noUsersYet()
	_ = loginTmpl.Execute(w, pageData{Title: "svkexe login", AllowRegister: allow})
}

// loginPost validates credentials and issues a session cookie on success.
func (s *Server) loginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	email := strings.TrimSpace(strings.ToLower(r.PostFormValue("email")))
	password := r.PostFormValue("password")

	fail := func(msg string) {
		w.WriteHeader(http.StatusUnauthorized)
		_ = loginTmpl.Execute(w, pageData{Title: "svkexe login", Error: msg, Email: email})
	}

	user, err := s.db.GetUserByEmail(email)
	if errors.Is(err, sql.ErrNoRows) {
		// Run a dummy bcrypt compare to keep response time roughly constant —
		// harder to enumerate valid accounts via timing.
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$12$zz................................................zz"), []byte(password))
		fail("Invalid email or password.")
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if user.PasswordHash == "" {
		fail("Account has no password set. Contact the operator.")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		fail("Invalid email or password.")
		return
	}

	sess, err := s.db.CreateSession(user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	setSessionCookie(w, sess.Token, int(dbpkg.SessionTTL.Seconds()))

	dest := r.URL.Query().Get("next")
	if dest == "" || !strings.HasPrefix(dest, "/") {
		dest = "/dashboard/"
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// logoutPost invalidates the caller's session and clears the cookie.
func (s *Server) logoutPost(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookieName); err == nil && c.Value != "" {
		_ = s.db.DeleteSession(c.Value)
	}
	clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// registerGet renders the first-time admin creation form, or redirects to
// /login if a user already exists.
func (s *Server) registerGet(w http.ResponseWriter, r *http.Request) {
	allow, err := s.noUsersYet()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !allow {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	_ = registerTmpl.Execute(w, pageData{Title: "Create admin account"})
}

// registerPost creates the first admin account. Only works while the users
// table is empty; all subsequent users are provisioned via the admin API.
func (s *Server) registerPost(w http.ResponseWriter, r *http.Request) {
	allow, err := s.noUsersYet()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !allow {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	email := strings.TrimSpace(strings.ToLower(r.PostFormValue("email")))
	password := r.PostFormValue("password")
	confirm := r.PostFormValue("password_confirm")

	fail := func(msg string) {
		w.WriteHeader(http.StatusBadRequest)
		_ = registerTmpl.Execute(w, pageData{Title: "Create admin account", Error: msg, Email: email})
	}

	if !strings.Contains(email, "@") {
		fail("Enter a valid email address.")
		return
	}
	if len(password) < 8 {
		fail("Password must be at least 8 characters.")
		return
	}
	if password != confirm {
		fail("Passwords do not match.")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	user := &dbpkg.User{
		ID:           uuid.NewString(),
		Email:        email,
		Role:         "admin",
		PasswordHash: string(hash),
	}
	if err := s.db.CreateUser(user); err != nil {
		fail("Could not create user: " + err.Error())
		return
	}
	sess, err := s.db.CreateSession(user.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	setSessionCookie(w, sess.Token, int(dbpkg.SessionTTL.Seconds()))
	http.Redirect(w, r, "/dashboard/", http.StatusSeeOther)
}

// noUsersYet returns true iff the users table is empty — used to gate the
// public /register flow.
func (s *Server) noUsersYet() (bool, error) {
	n, err := s.db.CountUsers()
	if err != nil {
		return false, err
	}
	return n == 0, nil
}
