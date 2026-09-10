package api

import (
	"encoding/json"
	"net/http"

	dbpkg "github.com/skrashevich/svkexe/internal/db"
)

// publishRequest is the JSON body for PUT /api/containers/{id}/publish.
type publishRequest struct {
	// Port is the in-VM port the bare VM host proxies to.
	Port int `json:"port"`
	// Public serves that port without a session.
	Public bool `json:"public"`
}

// updatePublish handles PUT /api/containers/{id}/publish. Ownership is enforced
// by OwnershipMiddleware on the parent route.
func (s *Server) updatePublish(w http.ResponseWriter, r *http.Request) {
	id := containerIDFromURL(r)

	var req publishRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Port == 0 {
		req.Port = dbpkg.DefaultAppPort
	}
	if !dbpkg.ValidAppPort(req.Port) {
		http.Error(w, "port must be between 1 and 65535 and must not be the agent port", http.StatusBadRequest)
		return
	}
	if err := s.db.UpdateContainerPublish(id, req.Port, req.Public); err != nil {
		http.Error(w, "failed to save publish settings", http.StatusInternalServerError)
		return
	}
	c, err := s.db.GetContainerByID(id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, c)
}
