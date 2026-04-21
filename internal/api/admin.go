package api

import (
	"database/sql"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// getMe handles GET /api/me — returns current user info.
func (s *Server) getMe(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, user)
}

// adminListUsers handles GET /api/admin/users — lists all users.
func (s *Server) adminListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.db.ListUsers()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, users)
}

// adminListContainers handles GET /api/admin/containers — lists all containers.
func (s *Server) adminListContainers(w http.ResponseWriter, r *http.Request) {
	containers, err := s.db.ListAllContainers()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, containers)
}

// adminDeleteUser handles DELETE /api/admin/users/{id} — deletes user and cascades.
func (s *Server) adminDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	_, err := s.db.GetUserByID(id)
	if err == sql.ErrNoRows {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := s.db.DeleteUser(id); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
