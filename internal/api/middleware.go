package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/skrashevich/svkexe/internal/ctxkeys"
	dbpkg "github.com/skrashevich/svkexe/internal/db"
)

// VM operations may unpack an image and initialize systemd for several minutes.
// Keep the response writable beyond the server's ordinary one-minute deadline.
func vmOperationDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead &&
			(r.URL.Path == "/api/containers" || strings.HasPrefix(r.URL.Path, "/api/containers/") ||
				r.URL.Path == "/dashboard/vms" || strings.HasPrefix(r.URL.Path, "/dashboard/vms/")) {
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Minute))
		}
		next.ServeHTTP(w, r)
	})
}

// AuthMiddleware validates the session cookie, loads the user, and injects
// it into the request context. API requests missing or with invalid sessions
// get a 401 JSON-ish response; HTML requests are redirected to /login.
func (s *Server) AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := s.authenticate(r)
		if user == nil {
			if wantsHTML(r) {
				dest := "/login"
				if r.URL.Path != "" {
					dest += "?next=" + r.URL.RequestURI()
				}
				http.Redirect(w, r, dest, http.StatusSeeOther)
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		if user.Role == dbpkg.GuestRole && !guestRequestAllowed(r) {
			http.Error(w, "guest accounts may only use assigned VMs and manage their SSH keys", http.StatusForbidden)
			return
		}
		ctx := context.WithValue(r.Context(), ctxkeys.UserID, user.ID)
		ctx = context.WithValue(ctx, ctxkeys.UserEmail, user.Email)
		ctx = context.WithValue(ctx, ctxkeys.User, user)

		// Downstream middlewares (rate limiter) and proxies (PicoClaw) still
		// consume X-ExeDev-Userid / X-ExeDev-Email. Inject them from the
		// authenticated session so we have a single source of truth.
		r.Header.Set("X-ExeDev-Userid", user.ID)
		r.Header.Set("X-ExeDev-Email", user.Email)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authenticate returns the user tied to the request's session cookie, or nil
// if the cookie is missing/invalid/expired.
func (s *Server) authenticate(r *http.Request) *dbpkg.User {
	c, err := r.Cookie(SessionCookieName)
	if err != nil || c.Value == "" {
		return nil
	}
	sess, err := s.db.GetSession(c.Value)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return nil
	}
	user, err := s.db.GetUserByID(sess.UserID)
	if err != nil {
		return nil
	}
	return user
}

// wantsHTML heuristically decides whether to send a redirect (for browsers)
// or a 401 body (for API clients / fetch / curl).
func wantsHTML(r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/metrics") {
		return false
	}
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "text/html") || accept == ""
}

// userIDFromCtx extracts the authenticated user ID from context.
func userIDFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(ctxkeys.UserID).(string)
	return v
}

// userFromCtx extracts the full User object from context.
func userFromCtx(ctx context.Context) *dbpkg.User {
	v, _ := ctx.Value(ctxkeys.User).(*dbpkg.User)
	return v
}

// AdminMiddleware checks that the authenticated user has the "admin" role.
// Returns 403 otherwise.
func (s *Server) AdminMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := userFromCtx(r.Context())
		if user == nil || user.Role != "admin" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// OwnershipMiddleware verifies that the container identified by {id} in the
// URL belongs to the authenticated user. A direct GET also permits named members.
// Must be used inside a chi route that
// has the {id} parameter.
func (s *Server) OwnershipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		containerID := containerIDFromURL(r)
		userID := userIDFromCtx(r.Context())

		c, err := s.db.GetContainerByID(containerID)
		if err == sql.ErrNoRows {
			http.Error(w, "container not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if c.OwnerID != userID && !(r.Method == http.MethodGet && strings.TrimSuffix(r.URL.Path, "/") == "/api/containers/"+containerID && s.db.CanUseContainer(c.ID, userID)) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Use an allowlist so future management endpoints cannot accidentally grant
// platform capabilities to restricted accounts.
func guestRequestAllowed(r *http.Request) bool {
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "/api/ssh-keys" || strings.HasPrefix(path, "/api/ssh-keys/") || path == "/dashboard/ssh-keys" || strings.HasPrefix(path, "/dashboard/ssh-keys/") {
		return true
	}
	if r.Method != http.MethodGet {
		return false
	}
	switch path {
	case "/api/me", "/api/containers", "/dashboard", "/dashboard/vms", "/dashboard/vms/list":
		return true
	}
	if rest, ok := strings.CutPrefix(path, "/api/containers/"); ok {
		return rest != "" && !strings.Contains(rest, "/")
	}
	if rest, ok := strings.CutPrefix(path, "/dashboard/vms/"); ok {
		parts := strings.Split(rest, "/")
		return len(parts) == 2 && (parts[1] == "shell" || parts[1] == "ws")
	}
	return false
}
