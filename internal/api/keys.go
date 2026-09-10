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
	chosen, err := s.db.UserDefaultModel(userID)
	if err != nil {
		return err
	}
	var syncErr error
	for _, c := range containers {
		if c.Status == "running" {
			syncErr = errors.Join(syncErr, picoclaw.RefreshProviderKeys(r.Context(), s.runtime, s.materializer, c.ID, c.IncusName, userID, chosen))
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
	// Protocol is the wire protocol the endpoint serves. Empty means the
	// OpenAI chat-completions shape, which is what every stored endpoint
	// predating this field was seeded as.
	Protocol string `json:"protocol"`
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
	baseURL, models, protocol, err := db.NormalizeProvider(req.Provider, req.BaseURL, req.Models, req.Key, req.Protocol)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	id := uuid.New().String()
	if err := s.db.SaveProviderKey(id, userID, req.Provider, req.Key, baseURL, models, protocol, s.encKey); err != nil {
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

// llmModelsResponse describes the models this owner may put their VMs on.
type llmModelsResponse struct {
	// Models are the agent-side IDs of the models reached through the owner's
	// own connections, most recently configured first.
	Models []string `json:"models"`
	// Default is the chosen model, empty when the gateway picks one.
	Default string `json:"default"`
}

// listLLMModels handles GET /api/llm/models
func (s *Server) listLLMModels(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r.Context())
	models, err := s.db.OwnerModelIDs(userID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	chosen, err := s.db.UserDefaultModel(userID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if models == nil {
		models = []string{}
	}
	writeJSON(w, http.StatusOK, llmModelsResponse{Models: models, Default: chosen})
}

// setDefaultModelRequest is the JSON body for choosing the default model.
type setDefaultModelRequest struct {
	// Model is an agent-side model ID from GET /api/llm/models, or the empty
	// string to hand the choice back to the gateway.
	Model string `json:"model"`
}

// putDefaultModel handles PUT /api/llm/default
func (s *Server) putDefaultModel(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r.Context())
	var req setDefaultModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	// Only an unreachable model is the caller's mistake; anything else is ours.
	changed, err := s.db.SetUserDefaultModel(userID, req.Model)
	switch {
	case errors.Is(err, db.ErrUnknownModel):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case errors.Is(err, sql.ErrNoRows):
		http.Error(w, "not found", http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, "failed to save the default model", http.StatusInternalServerError)
		return
	}
	// Syncing restarts the agent on every running VM, so a re-submitted choice
	// that changes nothing must not trigger it.
	if changed {
		if err := s.refreshKeysForUser(r, userID); err != nil {
			http.Error(w, "Default model saved, but VM sync failed; restart the VM to retry", http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, http.StatusOK, setDefaultModelRequest{Model: req.Model})
}
