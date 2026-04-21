package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/svkexe/platform/internal/ctxkeys"
	dbpkg "github.com/svkexe/platform/internal/db"
)

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

		ctx := context.WithValue(r.Context(), ctxkeys.UserID, user.ID)
		ctx = context.WithValue(ctx, ctxkeys.UserEmail, user.Email)
		ctx = context.WithValue(ctx, ctxkeys.User, user)

		// Downstream middlewares (rate limiter) and proxies (Shelley) still
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
// URL belongs to the authenticated user. Must be used inside a chi route that
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
		if c.OwnerID != userID {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
