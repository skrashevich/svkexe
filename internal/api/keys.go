package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/picoclaw"
)

// refreshKeysForUser re-materializes keys for all running containers owned by userID.
// A sync failure is reported separately from successful persistence.
func (s *Server) refreshKeysForUser(r *http.Request, userID string) error {
	if s.materializer == nil || s.runtime == nil {
		return nil
	}
	containers, err := s.db.ListContainersByOwner(userID)
	if err != nil {
		return err
	}
	var syncErr error
	for _, c := range containers {
		if c.Status == "running" {
			syncErr = errors.Join(syncErr, picoclaw.RefreshProviderKeys(r.Context(), s.runtime, s.materializer, c.ID, c.IncusName, userID))
		}
	}
	return syncErr
}

// createKeyRequest is the JSON body for API key creation.
type createKeyRequest struct {
	Provider string `json:"provider"`
	BaseURL  string `json:"base_url"`
	Models   string `json:"models"`
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
	req.Provider = strings.ToLower(strings.TrimSpace(req.Provider))
	baseURL, models, err := db.NormalizeProvider(req.Provider, req.BaseURL, req.Models, req.Key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	id := uuid.New().String()
	if err := s.db.SaveProviderKey(id, userID, req.Provider, req.Key, baseURL, models, s.encKey); err != nil {
		http.Error(w, "failed to store key", http.StatusInternalServerError)
		return
	}
	if err := s.refreshKeysForUser(r, userID); err != nil {
		http.Error(w, "Settings saved, but VM sync failed; restart the VM to retry", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

// deleteKey handles DELETE /api/keys/{id}
func (s *Server) deleteKey(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.db.DeleteAPIKeyForOwner(id, userID); errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "failed to delete key", http.StatusInternalServerError)
		return
	}
	if err := s.refreshKeysForUser(r, userID); err != nil {
		http.Error(w, "Settings saved, but VM sync failed; restart the VM to retry", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
