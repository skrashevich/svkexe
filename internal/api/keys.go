package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// refreshKeysForUser re-materializes keys for all running containers owned by userID.
// Errors are silently ignored since key refresh is best-effort.
func (s *Server) refreshKeysForUser(userID string) {
	if s.materializer == nil {
		return
	}
	containers, err := s.db.ListContainersByOwner(userID)
	if err != nil {
		return
	}
	for _, c := range containers {
		if c.Status == "running" {
			_ = s.materializer.RefreshKeys(c.ID, userID)
		}
	}
}

// createKeyRequest is the JSON body for API key creation.
type createKeyRequest struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
}

// listKeys handles GET /api/keys
func (s *Server) listKeys(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r.Context())
	keys, err := s.db.ListAPIKeysByOwner(userID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

// createKey handles POST /api/keys
func (s *Server) createKey(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r.Context())

	var req createKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Provider == "" || req.Key == "" {
		http.Error(w, "provider and key are required", http.StatusBadRequest)
		return
	}

	id := uuid.New().String()
	if err := s.db.CreateAPIKey(id, userID, req.Provider, req.Key, s.encKey); err != nil {
		http.Error(w, "failed to store key", http.StatusInternalServerError)
		return
	}
	s.refreshKeysForUser(userID)
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// deleteKey handles DELETE /api/keys/{id}
func (s *Server) deleteKey(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.db.DeleteAPIKey(id); err != nil {
		http.Error(w, "failed to delete key", http.StatusInternalServerError)
		return
	}
	s.refreshKeysForUser(userID)
	w.WriteHeader(http.StatusNoContent)
}
