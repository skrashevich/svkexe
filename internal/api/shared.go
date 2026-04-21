package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

type createSharedLinkRequest struct {
	ExpiresAt *time.Time `json:"expires_at"`
}

type sharedLinkResponse struct {
	Token     string     `json:"token"`
	URL       string     `json:"url"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// createSharedLink handles POST /api/containers/{id}/share
func (s *Server) createSharedLink(w http.ResponseWriter, r *http.Request) {
	containerID := containerIDFromURL(r)
	userID := userIDFromCtx(r.Context())

	var req createSharedLinkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && r.ContentLength != 0 {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Fetch container to get its name for building the URL.
	container, err := s.db.GetContainerByID(containerID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	link, err := s.db.CreateSharedLink(containerID, userID, req.ExpiresAt)
	if err != nil {
		http.Error(w, "failed to create shared link", http.StatusInternalServerError)
		return
	}

	resp := sharedLinkResponse{
		Token:     link.Token,
		URL:       fmt.Sprintf("https://%s.%s/?share=%s", container.Name, s.domain, link.Token),
		ExpiresAt: link.ExpiresAt,
	}
	writeJSON(w, http.StatusCreated, resp)
}

// listSharedLinks handles GET /api/containers/{id}/shares
func (s *Server) listSharedLinks(w http.ResponseWriter, r *http.Request) {
	containerID := containerIDFromURL(r)

	links, err := s.db.ListSharedLinksByContainer(containerID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, links)
}

// revokeSharedLink handles DELETE /api/shares/{token}
func (s *Server) revokeSharedLink(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if token == "" {
		http.Error(w, "token is required", http.StatusBadRequest)
		return
	}

	if err := s.db.DeleteSharedLinkByToken(token); err != nil {
		http.Error(w, "failed to revoke shared link", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
