package api

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/svkexe/platform/internal/ctxkeys"
	dbpkg "github.com/svkexe/platform/internal/db"
)

// AuthMiddleware reads X-ExeDev-Userid and X-ExeDev-Email headers, auto-provisions
// the user via EnsureUser, and injects the full User object into context.
// Returns 401 if the user ID header is absent.
func (s *Server) AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-ExeDev-Userid")
		if userID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		email := r.Header.Get("X-ExeDev-Email")

		user, err := s.db.EnsureUser(userID, email)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		ctx := context.WithValue(r.Context(), ctxkeys.UserID, userID)
		ctx = context.WithValue(ctx, ctxkeys.UserEmail, email)
		ctx = context.WithValue(ctx, ctxkeys.User, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
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
