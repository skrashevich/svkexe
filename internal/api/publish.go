package api

import (
	"encoding/json"
	"log"
	"net/http"

	dbpkg "github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/picoclaw"
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
	// Keep what the agent knows about its published port in step with what the
	// owner just set, without failing the save when the VM cannot be reached.
	if err := picoclaw.RefreshEnvironmentGuide(r.Context(), s.runtime, s.db, c); err != nil {
		log.Printf("publish settings for %s: refresh agent guide: %v", c.IncusName, err)
	}
	writeJSON(w, http.StatusOK, c)
}

// retryInitialTask handles POST /api/containers/{id}/task/retry. Ownership is
// enforced by OwnershipMiddleware on the parent route.
func (s *Server) retryInitialTask(w http.ResponseWriter, r *http.Request) {
	id := containerIDFromURL(r)
	if err := s.db.RetryInitialTask(id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Delivery records its own outcome, so the VM is re-read afterwards to
	// report whether this attempt actually reached the agent.
	picoclaw.DeliverInitialTaskByID(r.Context(), s.runtime, s.db, id, s.picoclawLLMCfg)
	c, err := s.db.GetContainerByID(id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, c)
}
